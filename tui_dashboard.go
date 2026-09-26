package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Red-based theme. Status colors (good/warn) stay green and amber so stream
// health reads the same as the Browser Source; errors use a filled red badge
// so they stand out from the red accents.
var (
	colorAccent  = lipgloss.AdaptiveColor{Light: "#C62828", Dark: "#EF5350"}
	colorAccent2 = lipgloss.AdaptiveColor{Light: "#E53935", Dark: "#FF8A80"}
	colorOnRed   = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#FFFFFF"}
	colorDim     = lipgloss.AdaptiveColor{Light: "#8D6E6E", Dark: "#A1887F"}
	colorGood    = lipgloss.AdaptiveColor{Light: "#2E7D32", Dark: "#8BC34A"}
	colorWarn    = lipgloss.AdaptiveColor{Light: "#B26A00", Dark: "#FFC107"}
	colorBad     = lipgloss.AdaptiveColor{Light: "#B71C1C", Dark: "#FF1744"}
	colorBorder  = lipgloss.AdaptiveColor{Light: "#EF9A9A", Dark: "#8E2A2A"}

	titleStyle        = lipgloss.NewStyle().Bold(true).Foreground(colorOnRed).Background(colorAccent).Padding(0, 1)
	accentStyle       = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	valueStyle        = lipgloss.NewStyle().Bold(true)
	sparkStyle        = lipgloss.NewStyle().Foreground(colorAccent2)
	dimStyle          = lipgloss.NewStyle().Foreground(colorDim)
	labelStyle        = lipgloss.NewStyle().Width(22)
	focusStyle        = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	tabStyle          = lipgloss.NewStyle().Padding(0, 1).Foreground(colorDim)
	activeTabStyle    = lipgloss.NewStyle().Padding(0, 1).Bold(true).Foreground(colorOnRed).Background(colorAccent)
	buttonStyle       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorBorder).Foreground(colorDim).Padding(0, 2)
	activeButtonStyle = buttonStyle.BorderForeground(colorAccent).Foreground(colorAccent).Bold(true)
	errorStyle        = lipgloss.NewStyle().Bold(true).Foreground(colorOnRed).Background(colorBad)
	badStyle          = lipgloss.NewStyle().Bold(true).Foreground(colorBad)
	warnStyle         = lipgloss.NewStyle().Foreground(colorWarn)
	goodStyle         = lipgloss.NewStyle().Foreground(colorGood)
	boxStyle          = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorBorder).Padding(0, 1)
	boxTitleStyle     = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
)

const (
	sampleHistory = 60
	staleAfter    = 3 * time.Second
)

type dashboardModel struct {
	cfg     config
	started time.Time
	now     time.Time
	width   int
	height  int

	samples []streamSample
	groups  []srtlaGroupInfo
	// linkRates holds each SRTLA link's receive rate in Mbps, computed from
	// the byte counters of the previous poll (prevGroups at prevPoll).
	linkRates  map[string]float64
	prevGroups []srtlaGroupInfo
	prevPoll   time.Time
	relay      *relaySnapshot
	output     udpOutputSnapshot

	logs     viewport.Model
	logLines []string

	stopped bool
	runErr  error
}

func newDashboardModel(cfg config, now time.Time) dashboardModel {
	return dashboardModel{cfg: cfg, started: now, now: now, logs: viewport.New(0, 0)}
}

func (d dashboardModel) Update(msg tea.Msg) (dashboardModel, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case streamSampleMsg:
		d = d.addSample(streamSample(msg))
		return d, nil, false
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc":
			return d, nil, true
		case "end", "G":
			d.logs.GotoBottom()
			return d, nil, false
		case "home", "g":
			d.logs.GotoTop()
			return d, nil, false
		}
	}
	var cmd tea.Cmd
	d.logs, cmd = d.logs.Update(msg)
	return d, cmd, false
}

func (d dashboardModel) addSample(s streamSample) dashboardModel {
	d.samples = append(d.samples, s)
	if over := len(d.samples) - sampleHistory; over > 0 {
		d.samples = append([]streamSample(nil), d.samples[over:]...)
	}
	return d.layout()
}

func (d dashboardModel) applyPoll(msg pollMsg) dashboardModel {
	d.now = msg.at
	d.linkRates = linkRates(d.prevGroups, msg.groups, msg.at.Sub(d.prevPoll))
	d.prevGroups, d.prevPoll = msg.groups, msg.at
	d.groups = msg.groups
	d.relay = msg.relay
	d.output = msg.output
	// In server mode the relay's publisher is the upstream leg; sample it the
	// same way the client does over the telemetry channel.
	if msg.relay != nil && msg.relay.Stats != nil {
		in := msg.relay.Stats.Instantaneous
		d = d.addSample(streamSample{At: msg.at, Bitrate: in.MbpsRecvRate, RTT: in.MsRTT, Loss: in.PktRecvLossRate})
	}
	return d.layout()
}

func (d dashboardModel) setLogs(lines []string) dashboardModel {
	atBottom := d.logs.AtBottom() || len(d.logLines) == 0
	d.logLines = lines
	d.logs.SetContent(strings.Join(lines, "\n"))
	if atBottom {
		d.logs.GotoBottom()
	}
	return d
}

func (d dashboardModel) resize(w, h int) dashboardModel {
	d.width, d.height = w, h
	return d.layout()
}

// layout sizes the log viewport to fill the space below the panels.
func (d dashboardModel) layout() dashboardModel {
	if d.width == 0 {
		return d
	}
	atBottom := d.logs.AtBottom()
	top := lipgloss.Height(d.header()) + lipgloss.Height(d.panels())
	h := d.height - top - 3 // log box border + footer
	if h < 3 {
		h = 3
	}
	d.logs.Width = d.width - 4
	d.logs.Height = h
	if atBottom {
		d.logs.GotoBottom()
	}
	return d
}

func (d dashboardModel) latest() (streamSample, bool) {
	if len(d.samples) == 0 {
		return streamSample{}, false
	}
	s := d.samples[len(d.samples)-1]
	return s, d.now.Sub(s.At) < staleAfter
}

func (d dashboardModel) header() string {
	mode := d.cfg.normalizedMode()
	uptime := d.now.Sub(d.started).Truncate(time.Second)
	line := titleStyle.Render("go-irl") + " " + accentStyle.Render(mode) + "  " + dimStyle.Render("up "+uptime.String())
	wrap := lipgloss.NewStyle().Width(max(d.width, 20))
	switch {
	case d.runErr != nil:
		line += "\n" + wrap.Inherit(badStyle).Render("✗ stopped: "+d.runErr.Error()+" (q to quit)")
	case d.stopped:
		line += "  " + dimStyle.Render("stopped")
	}
	for _, w := range d.cfg.warnings() {
		line += "\n" + wrap.Inherit(warnStyle).Render("⚠ "+w)
	}
	return line
}

const kvLabelWidth = 20

// kv renders a labelled row within width columns, moving the value to its own
// wrapped line when it does not fit beside the label.
func kv(width int, label, value string) string {
	if lipgloss.Width(value) <= width-kvLabelWidth {
		return dimStyle.Render(fmt.Sprintf("%-*s", kvLabelWidth, label)) + value
	}
	return dimStyle.Render(label) + "\n" + lipgloss.NewStyle().Width(max(width, 10)).Render(value)
}

func (d dashboardModel) endpointsPanel(width int) string {
	c := d.cfg
	var rows []string
	switch c.normalizedMode() {
	case "server":
		rows = append(rows,
			kv(width, "SRTLA input (phone)", fmt.Sprintf("UDP :%d", c.SRTLAPort)),
			kv(width, "SRT for client", fmt.Sprintf("UDP :%d", c.SRTPort)),
		)
	case "client":
		rows = append(rows,
			kv(width, "VPS", fmt.Sprintf("%s:%d", c.SRTHost, c.SRTPort)),
			kv(width, "UDP downstream", c.udpOutputURL()+dimStyle.Render("  (mpegts)")),
			kv(width, "Browser Source", c.browserSourceURL()),
		)
	default:
		rows = append(rows,
			kv(width, "SRTLA input (phone)", fmt.Sprintf("UDP :%d", c.SRTLAPort)),
			kv(width, "UDP downstream", c.udpOutputURL()+dimStyle.Render("  (mpegts)")),
			kv(width, "Browser Source", c.browserSourceURL()),
		)
	}
	rows = append(rows, kv(width, "Command", dimStyle.Render(c.commandLine())))
	return boxTitleStyle.Render("Endpoints") + "\n" + strings.Join(rows, "\n")
}

// linkRates returns the receive rate in Mbps of each link present in both
// snapshots, keyed by address. Links first seen in cur have no rate yet.
func linkRates(prev, cur []srtlaGroupInfo, elapsed time.Duration) map[string]float64 {
	if elapsed <= 0 {
		return nil
	}
	before := map[string]uint64{}
	for _, g := range prev {
		for _, c := range g.Conns {
			before[c.Addr] = c.RxBytes
		}
	}
	rates := map[string]float64{}
	for _, g := range cur {
		for _, c := range g.Conns {
			if b, ok := before[c.Addr]; ok && c.RxBytes >= b {
				rates[c.Addr] = float64(c.RxBytes-b) * 8 / elapsed.Seconds() / 1e6
			}
		}
	}
	return rates
}

func lossStyle(loss float64) lipgloss.Style {
	switch {
	case loss > 20:
		return badStyle
	case loss > 5:
		return warnStyle
	default:
		return goodStyle
	}
}

func sparkline(values []float64) string {
	const bars = "▁▂▃▄▅▆▇█"
	levels := []rune(bars)
	max := 0.0
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	var b strings.Builder
	for _, v := range values {
		i := 0
		if max > 0 {
			i = int(v / max * float64(len(levels)-1))
		}
		b.WriteRune(levels[i])
	}
	return b.String()
}

func (d dashboardModel) streamPanel(width int) string {
	mode := d.cfg.normalizedMode()
	var rows []string

	s, live := d.latest()
	switch {
	case mode == "server" && d.relay != nil && d.relay.PublisherAddr != "":
		rows = append(rows, kv(width, "Status", goodStyle.Render("● receiving")))
	case mode != "server" && live:
		rows = append(rows, kv(width, "Status", goodStyle.Render("● receiving")))
	default:
		rows = append(rows, kv(width, "Status", dimStyle.Render("○ waiting for stream")))
	}

	if live {
		rows = append(rows,
			kv(width, "Bitrate", valueStyle.Render(fmt.Sprintf("%.1f Mbps", s.Bitrate))),
			kv(width, "RTT", fmt.Sprintf("%.0f ms", s.RTT)),
			kv(width, "Loss", lossStyle(s.Loss).Render(fmt.Sprintf("%.1f %%", s.Loss))),
		)
		var bitrates []float64
		for _, x := range d.samples {
			bitrates = append(bitrates, x.Bitrate)
		}
		rows = append(rows, kv(width, "Bitrate (60s)", sparkStyle.Render(sparkline(bitrates))))
	}

	if mode != "server" {
		rows = append(rows, kv(width, "UDP downstream", d.outputStatus()))
	}

	if mode == "server" && d.relay != nil {
		rows = append(rows, kv(width, "Clients", fmt.Sprintf("%d stream · %d stats", d.relay.Subscribers, d.relay.StatsClients)))
	}

	if mode != "client" {
		rows = append(rows, "", boxTitleStyle.Render("SRTLA links"))
		addrWidth := 0
		for _, g := range d.groups {
			for _, c := range g.Conns {
				addrWidth = max(addrWidth, len(c.Addr))
			}
		}
		n := 0
		for _, g := range d.groups {
			for _, c := range g.Conns {
				ago := d.now.Sub(c.LastRcvd)
				style := goodStyle
				if ago > 2*time.Second {
					style = warnStyle
				}
				rate := "     -    "
				if r, ok := d.linkRates[c.Addr]; ok {
					rate = fmt.Sprintf("%5.1f Mbps", r)
				}
				row := style.Render("●") + " " + fmt.Sprintf("%-*s", addrWidth+2, c.Addr) + valueStyle.Render(rate)
				// Drop the last-received age rather than wrap the row.
				if since := "  " + dimStyle.Render(fmt.Sprintf("%.1fs ago", ago.Seconds())); lipgloss.Width(row+since) <= width {
					row += since
				}
				rows = append(rows, row)
				n++
			}
		}
		if n == 0 {
			rows = append(rows, dimStyle.Render("no connections"))
		}
	}

	return boxTitleStyle.Render("Stream") + "\n" + strings.Join(rows, "\n")
}

// outputStatus describes the UDP downstream output. UDP has no connection, so
// "sending" only means writes succeed; a refused write means nothing is
// listening on the port (e.g. the player is not running yet).
func (d dashboardModel) outputStatus() string {
	o := d.output
	switch {
	case !o.LastErr.IsZero() && d.now.Sub(o.LastErr) < udpOutputErrorHold:
		msg := "✗ no listener"
		if !strings.Contains(o.Err, "connection refused") {
			msg = "✗ " + o.Err
		}
		return badStyle.Render(msg) + dimStyle.Render("  (dropping packets)")
	case !o.LastOK.IsZero() && d.now.Sub(o.LastOK) < staleAfter:
		return goodStyle.Render("● sending")
	default:
		return dimStyle.Render("○ idle")
	}
}

func (d dashboardModel) panels() string {
	if d.width >= 120 {
		// Box Width includes padding but not the border.
		lw, rw := d.width*3/5-2, d.width-d.width*3/5-2
		left := d.endpointsPanel(lw - 2)
		right := d.streamPanel(rw - 2)
		// Match heights so the boxes line up.
		h := max(lipgloss.Height(left), lipgloss.Height(right))
		return lipgloss.JoinHorizontal(lipgloss.Top,
			boxStyle.Width(lw).Height(h).Render(left),
			boxStyle.Width(rw).Height(h).Render(right))
	}
	w := max(d.width-2, 20)
	return boxStyle.Width(w).Render(d.endpointsPanel(w-2)) + "\n" + boxStyle.Width(w).Render(d.streamPanel(w-2))
}

func (d dashboardModel) View() string {
	logBox := boxStyle.Width(max(d.width-2, 20)).Render(d.logs.View())
	footer := dimStyle.Render("q quit · ↑↓/pgup/pgdn scroll logs · g/G top/bottom")
	return lipgloss.JoinVertical(lipgloss.Left, d.header(), d.panels(), logBox, footer)
}
