package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	stats.Instantaneous.MsRecvBuf = 1400
	stats.Instantaneous.MsRecvTsbPdDelay = 2000
	stats.Accumulated.PktRecv = 900
	stats.Accumulated.PktRecvRetrans = 30
	stats.Accumulated.PktRecvBelated = 2
	reader, _ := json.Marshal(statsMessage{Type: "reader", Stats: stats})
	writer, _ := json.Marshal(statsMessage{Type: "writer", Stats: stats})

	s, ok := parseStreamSample(reader)
	if !ok || s.Bitrate != 6.5 || s.RTT != 42 || s.Loss != 3.25 ||
		s.BufferMs != 1400 || s.LatencyMs != 2000 || s.PktRecv != 900 || s.PktRetrans != 30 || s.PktLate != 2 {
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

func TestDashboardShowsUDPOutputStatus(t *testing.T) {
	start := time.Now()
	d := newDashboardModel(defaultTestConfig(), start).resize(100, 40)
	if view := d.View(); !strings.Contains(view, "idle") {
		t.Fatalf("output not idle before any write:\n%s", view)
	}

	var out udpOutput
	out.record(start, nil)
	out.record(start.Add(10*time.Millisecond), errors.New("write udp 127.0.0.1:1->127.0.0.1:5002: write: connection refused"))
	out.record(start.Add(20*time.Millisecond), nil) // flapping success does not mean recovery
	d = d.applyPoll(pollMsg{at: start.Add(time.Second), output: out.snapshot()})
	if view := d.View(); !strings.Contains(view, "no listener") {
		t.Fatalf("refused output not shown:\n%s", view)
	}

	out.record(start.Add(3*time.Second), nil)
	d = d.applyPoll(pollMsg{at: start.Add(3 * time.Second), output: out.snapshot()})
	if view := d.View(); !strings.Contains(view, "sending") {
		t.Fatalf("recovered output not shown:\n%s", view)
	}
}

func TestDashboardShowsSRTLALinkRates(t *testing.T) {
	start := time.Now()
	d := newDashboardModel(defaultTestConfig(), start).resize(100, 40)
	snap := func(a, b uint64) []srtlaGroupInfo {
		return []srtlaGroupInfo{{Conns: []srtlaConnInfo{
			{Addr: "203.0.113.5:40123", LastRcvd: start, RxBytes: a},
			{Addr: "198.51.100.7:51022", LastRcvd: start, RxBytes: b},
		}}}
	}
	d = d.applyPoll(pollMsg{at: start, groups: snap(0, 0)})
	d = d.applyPoll(pollMsg{at: start.Add(time.Second), groups: snap(400_000, 225_000)})
	view := d.View()
	if !strings.Contains(view, "3.2 Mbps") || !strings.Contains(view, "1.8 Mbps") {
		t.Fatalf("link rates not shown:\n%s", view)
	}
}

func TestDashboardSplitsLossIntoRecoveredAndLate(t *testing.T) {
	start := time.Now()
	d := newDashboardModel(defaultTestConfig(), start).resize(100, 40)
	d, _, _ = d.Update(streamSampleMsg{At: start, Loss: 5, PktRecv: 1000, PktRetrans: 10, PktLate: 1, BufferMs: 1500, LatencyMs: 2000})
	d, _, _ = d.Update(streamSampleMsg{At: start.Add(time.Second), Loss: 5, PktRecv: 1500, PktRetrans: 35, PktLate: 4, BufferMs: 1500, LatencyMs: 2000})
	view := d.View()
	// 25 retransmissions, 3 of them too late: 22 of 500 packets recovered.
	for _, want := range []string{"4.4 %  22 pkts", "3 pkts  4 total", "1.5s / 2.0s"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}

	// Counters going backwards mean the stream reconnected; no bogus interval.
	d, _, _ = d.Update(streamSampleMsg{At: start.Add(2 * time.Second), PktRecv: 10})
	if recv, _, _, ok := d.lastInterval(); ok {
		t.Fatalf("interval across a reconnect: recv=%d", recv)
	}
}

func TestDashboardRecordsEvents(t *testing.T) {
	start := time.Now()
	d := newDashboardModel(defaultTestConfig(), start).resize(140, 40)
	link := func(addr string, last time.Time) srtlaConnInfo { return srtlaConnInfo{Addr: addr, LastRcvd: last} }

	d, _, _ = d.Update(streamSampleMsg{At: start, Loss: 30})
	d = d.applyPoll(pollMsg{at: start, groups: []srtlaGroupInfo{{Conns: []srtlaConnInfo{link("a:1", start), link("b:2", start)}}}})
	d = d.applyPoll(pollMsg{at: start.Add(5 * time.Second), groups: []srtlaGroupInfo{{Conns: []srtlaConnInfo{link("a:1", start)}}}})

	var got []string
	for _, e := range d.events {
		got = append(got, e.Text)
	}
	want := []string{"stream started", "loss spike 30.0 %", "link joined a:1", "link joined b:2", "stream lost", "link stalled a:1", "link left b:2"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %q, want %q", got, want)
	}
	if view := d.View(); !strings.Contains(view, "link left b:2") {
		t.Fatalf("events not shown:\n%s", view)
	}
}

// The dashboard must never draw wider or taller than the terminal, whichever
// layout the width selects.
func TestDashboardFitsTerminal(t *testing.T) {
	start := time.Now()
	for _, mode := range []string{"standalone", "client", "server"} {
		for _, w := range []int{160, 120, 100, 80, 60} {
			for _, h := range []int{50, 24} {
				c := defaultTestConfig()
				c.Mode = mode
				d := newDashboardModel(c, start).resize(w, h)
				for i := range 5 {
					at := start.Add(time.Duration(i) * time.Second)
					d, _, _ = d.Update(streamSampleMsg{At: at, Bitrate: 6, RTT: 40, Loss: 3, PktRecv: uint64(i * 500)})
					d = d.applyPoll(pollMsg{
						at: at,
						groups: []srtlaGroupInfo{{Conns: []srtlaConnInfo{
							{Addr: "203.0.113.4:51002", LastRcvd: at, RxBytes: uint64(i) * 500_000},
							{Addr: "198.51.100.7:40211", LastRcvd: at, RxBytes: uint64(i) * 200_000},
						}}},
						relay: &relaySnapshot{PublisherAddr: "192.0.2.1:4000", Subscribers: 1},
					})
				}
				d = d.setLogs([]string{strings.Repeat("long log line ", 20)})
				lines := strings.Split(d.View(), "\n")
				if len(lines) > h {
					t.Errorf("%s %dx%d: %d lines", mode, w, h, len(lines))
				}
				for _, l := range lines {
					if lipgloss.Width(l) > w {
						t.Errorf("%s %dx%d: line wider than terminal: %q", mode, w, h, l)
						break
					}
				}
			}
		}
	}
}

func TestDashboardLayoutTiers(t *testing.T) {
	start := time.Now()
	for _, tc := range []struct {
		width      int
		charts     bool // bitrate chart panel
		endpoints  bool
		eventsPane bool
	}{
		{140, true, true, true},
		{100, true, true, true},
		{60, false, false, false},
	} {
		view := newDashboardModel(defaultTestConfig(), start).resize(tc.width, 60).View()
		for name, want := range map[string]bool{"Bitrate ─": tc.charts, "Endpoints": tc.endpoints, "Events": tc.eventsPane} {
			if strings.Contains(view, name) != want {
				t.Errorf("width %d: shows %q = %v, want %v\n%s", tc.width, name, !want, want, view)
			}
		}
	}
}

func TestSparklineUsesFixedScale(t *testing.T) {
	// The same value keeps its height whatever else is in the window.
	if a, b := sparkline([]float64{5}, 1, 10), sparkline([]float64{5, 0.1}, 2, 10)[:len("▄")]; a != b || a != "▄" {
		t.Fatalf("sparkline 5/10 = %q and %q, want ▄", a, b)
	}
	if got := sparkline([]float64{0, 10, 20}, 5, 10); got != "  ▁██" {
		t.Fatalf("sparkline = %q", got)
	}
}

func TestBrailleChart(t *testing.T) {
	// Left column full, right column empty.
	if got := brailleChart([]float64{10, 0}, 1, 1, 10); got[0] != string(rune(0x2847)) {
		t.Fatalf("chart = %q", got)
	}
	// Half of two rows fills the bottom row only; history is right-aligned.
	got := brailleChart([]float64{5}, 2, 2, 10)
	if got[0] != "⠀⠀" || got[1] != "⠀"+string(rune(0x28B8)) {
		t.Fatalf("chart = %q", got)
	}
}

func TestShareBarFillsWidth(t *testing.T) {
	for _, rates := range [][]float64{{1, 1, 1}, {3.4, 2.1, 1.0}, {5, 0}, {0, 0}} {
		if w := lipgloss.Width(shareBar(rates, 37)); w != 37 {
			t.Errorf("shareBar(%v) width = %d", rates, w)
		}
	}
	if got := shareBar([]float64{3, 1}, 8); got != strings.Repeat("█", 6)+strings.Repeat("▓", 2) {
		t.Fatalf("shareBar = %q", got)
	}
}

func TestNiceCeil(t *testing.T) {
	for v, want := range map[float64]float64{0.3: 0.5, 7: 10, 10: 10, 12: 20, 180: 200, 260: 500} {
		if got := niceCeil(v); got != want {
			t.Errorf("niceCeil(%v) = %v, want %v", v, got, want)
		}
	}
}
