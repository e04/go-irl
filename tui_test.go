package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	srt "github.com/datarhei/gosrt"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(m setupModel, keys ...string) setupModel {
	for _, k := range keys {
		m, _ = m.Update(key(k))
	}
	return m
}

func TestSetupShowsFieldsForMode(t *testing.T) {
	m := newSetupModel(defaultTestConfig())
	if m.mode() != "standalone" {
		t.Fatalf("mode = %q, want standalone", m.mode())
	}
	view := m.View()
	if strings.Contains(view, "VPS host") || !strings.Contains(view, "Browser Source port") {
		t.Fatalf("standalone form shows wrong fields:\n%s", view)
	}

	m = press(m, "right", "right") // client
	view = m.View()
	if !strings.Contains(view, "VPS host") || strings.Contains(view, "SRTLA port") {
		t.Fatalf("client form shows wrong fields:\n%s", view)
	}
}

func TestSetupServerRequiresPassphraseUnlessInsecure(t *testing.T) {
	m := press(newSetupModel(defaultTestConfig()), "right") // server
	m = press(m, "up", "enter")                             // Start
	if m.done || m.err == nil || !strings.Contains(m.err.Error(), "passphrase") {
		t.Fatalf("done=%v err=%v, want passphrase error", m.done, m.err)
	}

	m = press(m, "up", "up", " ")       // "Allow no passphrase"
	m = press(m, "tab", "tab", "enter") // past "Verbose" to Start
	if !m.done || m.err != nil {
		t.Fatalf("done=%v err=%v, want started", m.done, m.err)
	}
	if m.cfg.Mode != "server" || !m.cfg.Insecure {
		t.Fatalf("cfg = %+v", m.cfg)
	}
}

func TestSetupPortFieldsAcceptDigitsOnly(t *testing.T) {
	c := defaultTestConfig()
	c.SRTLAPort = 0
	m := press(newSetupModel(c), "tab") // SRTLA port
	m.fields[m.focusedField()].input.SetValue("")
	m = press(m, "7", "x", "0", "0", "0")
	got, err := m.toConfig()
	if err != nil || got.SRTLAPort != 7000 {
		t.Fatalf("SRTLAPort = %d, err = %v, want 7000", got.SRTLAPort, err)
	}
}

func TestParseStreamSample(t *testing.T) {
	stats := &srt.Statistics{}
	stats.Instantaneous.MbpsRecvRate = 6.5
	stats.Instantaneous.MsRTT = 42
	stats.Instantaneous.PktRecvLossRate = 3.25
	reader, _ := json.Marshal(statsMessage{Type: "reader", Stats: stats})
	writer, _ := json.Marshal(statsMessage{Type: "writer", Stats: stats})

	s, ok := parseStreamSample(reader)
	if !ok || s.Bitrate != 6.5 || s.RTT != 42 || s.Loss != 3.25 {
		t.Fatalf("parseStreamSample = %+v, %v", s, ok)
	}
	if _, ok := parseStreamSample(writer); ok {
		t.Fatal("writer statistics should be ignored")
	}
	if _, ok := parseStreamSample(telemetryHeartbeat); ok {
		t.Fatal("heartbeat should be ignored")
	}
}

func TestLogSinkSplitsAndTrimsLines(t *testing.T) {
	notified := 0
	s := &logSink{notify: func() { notified++ }}
	s.Write([]byte("first\nsec"))
	s.Write([]byte("ond\r\n"))
	if got := s.snapshot(); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("lines = %q", got)
	}
	if notified != 2 {
		t.Fatalf("notified %d times, want 2", notified)
	}

	for i := 0; i < maxLogLines+10; i++ {
		s.Write([]byte("x\n"))
	}
	if got := len(s.snapshot()); got != maxLogLines {
		t.Fatalf("kept %d lines, want %d", got, maxLogLines)
	}
}

func TestDashboardMarksStaleStream(t *testing.T) {
	start := time.Now()
	d := newDashboardModel(defaultTestConfig(), start).resize(100, 40)
	d, _, _ = d.Update(streamSampleMsg{At: start, Bitrate: 4, RTT: 30, Loss: 25})
	if view := d.View(); !strings.Contains(view, "receiving") || !strings.Contains(view, "25.0 %") {
		t.Fatalf("live stream not shown:\n%s", view)
	}

	d = d.applyPoll(pollMsg{at: start.Add(10 * time.Second)})
	if view := d.View(); !strings.Contains(view, "waiting for stream") {
		t.Fatalf("stale stream still shown as live:\n%s", view)
	}
}
