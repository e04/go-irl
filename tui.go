package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const maxLogLines = 500

// logSink is an io.Writer for the standard logger that keeps the most recent
// lines in memory for the dashboard. Writes never block on the UI.
type logSink struct {
	mu      sync.Mutex
	lines   []string
	partial []byte
	notify  func()
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			break
		}
		s.lines = append(s.lines, strings.TrimRight(string(s.partial[:i]), "\r"))
		s.partial = s.partial[i+1:]
	}
	if over := len(s.lines) - maxLogLines; over > 0 {
		s.lines = append([]string(nil), s.lines[over:]...)
	}
	notify := s.notify
	s.mu.Unlock()

	if notify != nil {
		notify()
	}
	return len(p), nil
}

func (s *logSink) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

// tuiBridge carries events from the running mode's goroutines to the Bubble
// Tea program without ever blocking them.
type tuiBridge struct {
	events chan tea.Msg
	relay  atomic.Pointer[srtRelay]
	output udpOutput
}

func newTUIBridge() *tuiBridge {
	return &tuiBridge{events: make(chan tea.Msg, 64)}
}

// post queues msg for the UI, dropping it if the UI is behind. Everything
// posted is also refreshed by the periodic poll, so drops are harmless.
func (b *tuiBridge) post(msg tea.Msg) {
	select {
	case b.events <- msg:
	default:
	}
}

func (b *tuiBridge) forward(ctx context.Context, p *tea.Program) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-b.events:
			p.Send(msg)
		}
	}
}

func (b *tuiBridge) hooks() runHooks {
	return runHooks{
		onStats: func(payload []byte) {
			if sample, ok := parseStreamSample(payload); ok {
				b.post(streamSampleMsg(sample))
			}
		},
		onRelay: func(r *srtRelay) { b.relay.Store(r) },
		output:  &b.output,
	}
}

// streamSample holds the values the Browser Source overlay shows.
type streamSample struct {
	At      time.Time
	Bitrate float64 // Mbps
	RTT     float64 // ms
	Loss    float64 // percent
}

// parseStreamSample extracts the displayed values from a statistics message,
// matching the Browser Source (frontend/src/App.tsx).
func parseStreamSample(payload []byte) (streamSample, bool) {
	var msg statsMessage
	if err := json.Unmarshal(payload, &msg); err != nil || msg.Type != "reader" || msg.Stats == nil {
		return streamSample{}, false
	}
	return streamSample{
		At:      time.Now(),
		Bitrate: msg.Stats.Instantaneous.MbpsRecvRate,
		RTT:     msg.Stats.Instantaneous.MsRTT,
		Loss:    msg.Stats.Instantaneous.PktRecvLossRate,
	}, true
}

type (
	streamSampleMsg streamSample
	logsChangedMsg  struct{}
	runExitedMsg    struct{ err error }
	startMsg        struct{ cfg config }
	pollMsg         struct {
		at     time.Time
		groups []srtlaGroupInfo
		relay  *relaySnapshot
		output udpOutputSnapshot
	}
)

func pollCmd(b *tuiBridge) tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		msg := pollMsg{at: t, groups: srtlaSnapshot(), output: b.output.snapshot()}
		if r := b.relay.Load(); r != nil {
			snap := r.snapshot()
			msg.relay = &snap
		}
		return msg
	})
}

type screen int

const (
	screenSetup screen = iota
	screenDashboard
)

type appModel struct {
	screen    screen
	setup     setupModel
	dashboard dashboardModel
	width     int
	height    int

	bridge *tuiBridge
	logs   *logSink
	start  func(config) // launches runMode in the background

	runErr error
}

func (m appModel) Init() tea.Cmd {
	if m.screen == screenSetup {
		return m.setup.Init()
	}
	return func() tea.Msg { return startMsg{cfg: m.setup.cfg} }
}

func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.dashboard = m.dashboard.resize(msg.Width, msg.Height)
		m.setup.width = msg.Width
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	case startMsg:
		m.screen = screenDashboard
		m.dashboard = newDashboardModel(msg.cfg, time.Now()).resize(m.width, m.height)
		if m.start != nil {
			m.start(msg.cfg)
		}
		return m, pollCmd(m.bridge)

	case runExitedMsg:
		m.runErr = msg.err
		m.dashboard.runErr = msg.err
		m.dashboard.stopped = true
		if msg.err == nil {
			return m, tea.Quit
		}
		return m, nil

	case pollMsg:
		if m.logs != nil {
			m.dashboard = m.dashboard.setLogs(m.logs.snapshot())
		}
		m.dashboard = m.dashboard.applyPoll(msg)
		return m, pollCmd(m.bridge)

	case logsChangedMsg:
		if m.logs != nil {
			m.dashboard = m.dashboard.setLogs(m.logs.snapshot())
		}
		return m, nil
	}

	switch m.screen {
	case screenSetup:
		var cmd tea.Cmd
		m.setup, cmd = m.setup.Update(msg)
		if m.setup.cancelled {
			return m, tea.Quit
		}
		if m.setup.done {
			cfg := m.setup.cfg
			return m, func() tea.Msg { return startMsg{cfg: cfg} }
		}
		return m, cmd
	default:
		var cmd tea.Cmd
		var quit bool
		m.dashboard, cmd, quit = m.dashboard.Update(msg)
		if quit {
			return m, tea.Quit
		}
		return m, cmd
	}
}

func (m appModel) View() string {
	if m.screen == screenSetup {
		return m.setup.View()
	}
	return m.dashboard.View()
}

// runTUI runs go-irl with the interactive terminal UI. When showSetup is false
// and cfg is valid, it starts immediately; otherwise the setup form is shown
// first, prefilled with cfg.
func runTUI(cfg config, showSetup bool) error {
	validationErr := cfg.validate()
	setup := newSetupModel(cfg)
	if validationErr != nil && !showSetup {
		setup.err = validationErr
	}

	bridge := newTUIBridge()
	logs := &logSink{notify: func() { bridge.post(logsChangedMsg{}) }}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var runDone chan struct{}
	model := appModel{
		screen: screenSetup,
		setup:  setup,
		bridge: bridge,
		logs:   logs,
	}
	model.start = func(c config) {
		runDone = make(chan struct{})
		go func() {
			defer close(runDone)
			err := runMode(ctx, c, bridge.hooks())
			select { // unlike post, never drop this one
			case bridge.events <- runExitedMsg{err: err}:
			case <-ctx.Done():
			}
		}()
	}
	if !showSetup && validationErr == nil {
		model.screen = screenDashboard
	}

	prevOutput, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(logs)
	defer func() {
		log.SetOutput(prevOutput)
		log.SetFlags(prevFlags)
	}()

	p := tea.NewProgram(model, tea.WithAltScreen())
	go bridge.forward(ctx, p)

	final, err := p.Run()
	cancel()
	if runDone != nil {
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		return err
	}

	fm := final.(appModel)
	if fm.runErr != nil {
		printRecentLogs(os.Stderr, logs.snapshot(), 20)
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", fm.runErr)
		waitForEnter(os.Stdin, os.Stdout)
		os.Exit(1)
	}
	return nil
}

func printRecentLogs(w io.Writer, lines []string, n int) {
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
}

// waitForEnter keeps the window open so errors stay readable when go-irl was
// started by double-clicking.
func waitForEnter(r io.Reader, w io.Writer) {
	fmt.Fprint(w, "Press Enter to exit...")
	_, _ = bufio.NewReader(r).ReadString('\n')
}
