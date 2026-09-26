package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var setupModes = []struct {
	name, desc string
}{
	{"standalone", "SRTLA receiver and OBS output on this machine (needs UDP port forwarding)"},
	{"server", "Run on a VPS: receive SRTLA and relay SRT to the client"},
	{"client", "Run next to OBS: connect to the VPS, no inbound port needed"},
}

type fieldKind int

const (
	fieldText fieldKind = iota
	fieldPort
	fieldSecret
	fieldToggle
)

type setupField struct {
	flag  string
	label string
	kind  fieldKind
	modes []string // modes the field applies to
	input textinput.Model
	on    bool // fieldToggle value
}

func (f setupField) appliesTo(mode string) bool {
	return slices.Contains(f.modes, mode)
}

type setupModel struct {
	modeIdx  int
	fields   []setupField
	focus    int // 0 = mode selector, 1..n = visible fields, n+1 = Start
	showPass bool
	width    int

	cfg       config // valid once done
	err       error
	done      bool
	cancelled bool
}

func newSetupModel(cfg config) setupModel {
	newInput := func(value string, limit int) textinput.Model {
		in := textinput.New()
		in.Prompt = ""
		in.CharLimit = limit
		in.SetValue(value)
		return in
	}
	port := func(v int) textinput.Model { return newInput(strconv.Itoa(v), 5) }

	pass := newInput(cfg.Passphrase, 79)
	pass.EchoMode = textinput.EchoPassword
	pass.EchoCharacter = '•'

	m := setupModel{
		fields: []setupField{
			{flag: "srt-host", label: "VPS host", kind: fieldText, modes: []string{"client"}, input: newInput(cfg.SRTHost, 253)},
			{flag: "srt-port", label: "SRT port", kind: fieldPort, modes: []string{"server", "client"}, input: port(cfg.SRTPort)},
			{flag: "srtla-port", label: "SRTLA port", kind: fieldPort, modes: []string{"server", "standalone"}, input: port(cfg.SRTLAPort)},
			{flag: "udp-port", label: "OBS UDP port", kind: fieldPort, modes: []string{"client", "standalone"}, input: port(cfg.UDPPort)},
			{flag: "bs-port", label: "Browser Source port", kind: fieldPort, modes: []string{"client", "standalone"}, input: port(cfg.BSPort)},
			{flag: "ws-port", label: "WebSocket port", kind: fieldPort, modes: []string{"client", "standalone"}, input: port(cfg.WSPort)},
			{flag: "passphrase", label: "Passphrase", kind: fieldSecret, modes: []string{"server", "client", "standalone"}, input: pass},
			{flag: "insecure", label: "Allow no passphrase", kind: fieldToggle, modes: []string{"server"}, on: cfg.Insecure},
			{flag: "verbose", label: "Verbose SRTLA logs", kind: fieldToggle, modes: []string{"server", "standalone"}, on: cfg.Verbose},
		},
		cfg: cfg,
	}
	for i, md := range setupModes {
		if md.name == cfg.normalizedMode() {
			m.modeIdx = i
		}
	}
	return m
}

func (m setupModel) mode() string { return setupModes[m.modeIdx].name }

// visible returns the indexes of fields shown for the selected mode.
func (m setupModel) visible() []int {
	var idx []int
	for i, f := range m.fields {
		if f.appliesTo(m.mode()) {
			idx = append(idx, i)
		}
	}
	return idx
}

func (m setupModel) startFocus() int { return len(m.visible()) + 1 }

// focusedField returns the index into m.fields of the focused field, or -1.
func (m setupModel) focusedField() int {
	vis := m.visible()
	if m.focus >= 1 && m.focus <= len(vis) {
		return vis[m.focus-1]
	}
	return -1
}

func (m setupModel) Init() tea.Cmd { return textinput.Blink }

// toConfig builds a config from the form, keeping values of fields hidden
// for the selected mode.
func (m setupModel) toConfig() (config, error) {
	cfg := m.cfg
	cfg.Mode = m.mode()
	for _, f := range m.fields {
		v := strings.TrimSpace(f.input.Value())
		if f.kind == fieldPort && f.appliesTo(cfg.Mode) {
			n, err := strconv.Atoi(v)
			if err != nil {
				return cfg, fmt.Errorf("%s must be a number (1-65535)", f.label)
			}
			switch f.flag {
			case "srt-port":
				cfg.SRTPort = n
			case "srtla-port":
				cfg.SRTLAPort = n
			case "udp-port":
				cfg.UDPPort = n
			case "bs-port":
				cfg.BSPort = n
			case "ws-port":
				cfg.WSPort = n
			}
			continue
		}
		switch f.flag {
		case "srt-host":
			cfg.SRTHost = v
		case "passphrase":
			cfg.Passphrase = f.input.Value()
		case "insecure":
			cfg.Insecure = f.on
		case "verbose":
			cfg.Verbose = f.on
		}
	}
	return cfg, nil
}

func (m setupModel) setFocus(focus int) (setupModel, tea.Cmd) {
	last := m.startFocus()
	if focus < 0 {
		focus = last
	} else if focus > last {
		focus = 0
	}
	m.focus = focus
	var cmd tea.Cmd
	fi := m.focusedField()
	for i := range m.fields {
		if i == fi && m.fields[i].kind != fieldToggle {
			cmd = m.fields[i].input.Focus()
		} else {
			m.fields[i].input.Blur()
		}
	}
	return m, cmd
}

func (m setupModel) Update(msg tea.Msg) (setupModel, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		if fi := m.focusedField(); fi >= 0 {
			var cmd tea.Cmd
			m.fields[fi].input, cmd = m.fields[fi].input.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	fi := m.focusedField()
	switch key.String() {
	case "esc":
		m.cancelled = true
		return m, nil
	case "tab", "down":
		return m.setFocus(m.focus + 1)
	case "shift+tab", "up":
		return m.setFocus(m.focus - 1)
	case "ctrl+r":
		m.showPass = !m.showPass
		for i := range m.fields {
			if m.fields[i].kind == fieldSecret {
				if m.showPass {
					m.fields[i].input.EchoMode = textinput.EchoNormal
				} else {
					m.fields[i].input.EchoMode = textinput.EchoPassword
				}
			}
		}
		return m, nil
	case "left", "right":
		if m.focus == 0 {
			d := 1
			if key.String() == "left" {
				d = len(setupModes) - 1
			}
			m.modeIdx = (m.modeIdx + d) % len(setupModes)
			m.err = nil
			return m, nil
		}
	case "enter":
		if m.focus == m.startFocus() {
			return m.submit(), nil
		}
		if fi >= 0 && m.fields[fi].kind == fieldToggle {
			m.fields[fi].on = !m.fields[fi].on
			return m, nil
		}
		return m.setFocus(m.focus + 1)
	case " ":
		if fi >= 0 && m.fields[fi].kind == fieldToggle {
			m.fields[fi].on = !m.fields[fi].on
			return m, nil
		}
	}

	if fi < 0 || m.fields[fi].kind == fieldToggle {
		return m, nil
	}
	if m.fields[fi].kind == fieldPort && key.Type == tea.KeyRunes {
		for _, r := range key.Runes {
			if r < '0' || r > '9' {
				return m, nil
			}
		}
	}
	var cmd tea.Cmd
	m.fields[fi].input, cmd = m.fields[fi].input.Update(msg)
	return m, cmd
}

func (m setupModel) submit() setupModel {
	cfg, err := m.toConfig()
	if err == nil {
		err = cfg.validate()
	}
	m.err = err
	if err == nil {
		m.cfg = cfg
		m.done = true
	}
	return m
}

func (m setupModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("go-irl") + " " + accentStyle.Render("setup") + "\n\n")

	// Mode selector
	var tabs []string
	for i, md := range setupModes {
		style := tabStyle
		if i == m.modeIdx {
			style = activeTabStyle
		}
		tabs = append(tabs, style.Render(md.name))
	}
	cursor := "  "
	if m.focus == 0 {
		cursor = focusStyle.Render("▸ ")
	}
	// Tabs carry one column of padding; pull them left so their text lines up
	// with the field values below.
	modeLabel := labelStyle.Width(labelStyle.GetWidth() - 1).Render("Mode")
	b.WriteString(cursor + modeLabel + strings.Join(tabs, " ") + "\n")
	descWidth := max(m.width-4-2-labelStyle.GetWidth(), 20) // minus view padding, cursor, label
	desc := lipgloss.NewStyle().Width(descWidth).Inherit(dimStyle).Render(setupModes[m.modeIdx].desc)
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, "  ", labelStyle.Render(""), desc) + "\n\n")

	for n, i := range m.visible() {
		f := m.fields[i]
		cursor := "  "
		if m.focus == n+1 {
			cursor = focusStyle.Render("▸ ")
		}
		var value string
		if f.kind == fieldToggle {
			if f.on {
				value = "[x]"
			} else {
				value = "[ ]"
			}
		} else {
			value = f.input.View()
		}
		hint := ""
		if f.kind == fieldSecret {
			hint = dimStyle.Render("  (min 10 chars; ctrl+r show/hide)")
		}
		b.WriteString(cursor + labelStyle.Render(f.label) + value + hint + "\n")
	}

	b.WriteString("\n")
	start := buttonStyle.Render("Start")
	cursor = "  "
	if m.focus == m.startFocus() {
		start = activeButtonStyle.Render("Start")
		cursor = focusStyle.Render("▸ ")
	}
	// Join as blocks so every line of the bordered button shares the indent.
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Center, cursor, start) + "\n")

	if m.err != nil {
		b.WriteString("\n" + errorStyle.Render(" ✗ "+m.err.Error()+" ") + "\n")
	}

	b.WriteString("\n" + dimStyle.Render("tab/↑↓ move · ←→ mode · space toggle · enter start · esc quit"))
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}
