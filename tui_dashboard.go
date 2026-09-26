package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
	boxTitleStyle     = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
)

const (
	sampleHistory  = 600 // 10 minutes of 1 s reports
	linkHistory    = 120
	maxEvents      = 200
	staleAfter     = 3 * time.Second
	linkStallAfter = 2 * time.Second

	// Loss thresholds shared with the Browser Source (connectionQuality.ts).
	highLoss = 20.0
	lowLoss  = 5.0

	// Terminal widths at which the dashboard switches layout.
	mediumWidth = 80
	wideWidth   = 120
)

// scales are the upper bounds of the charts. They start at scaleFloors and
// only grow, in round steps, when a value exceeds them, so the same value
// keeps the same height and quiet periods don't look dramatic.
type scales struct{ bitrate, rtt, loss, link float64 }

var scaleFloors = scales{bitrate: 10, rtt: 200, loss: 20, link: 5}

type dashboardModel struct {
	cfg     config
	started time.Time
	now     time.Time
	width   int
	height  int

	samples []streamSample
	scale   scales
	groups  []srtlaGroupInfo
	// linkRates holds each SRTLA link's receive rate in Mbps, computed from
	// the byte counters of the previous poll (prevGroups at prevPoll).
	linkRates  map[string]float64
	prevGroups []srtlaGroupInfo
	prevPoll   time.Time
	// linkHist and linkSeen hold each link's recent rates and when it was
	// first seen.
	linkHist map[string][]float64
	linkSeen map[string]time.Time
	relay    *relaySnapshot
	output   udpOutputSnapshot

	events []dashEvent
	watch  watchState

	logs     viewport.Model
	logLines []string
	// hideEndpoints drops the Endpoints panel when the terminal is too short
	// to fit it above the logs.
	hideEndpoints bool

	stopped bool
	runErr  error
}

func newDashboardModel(cfg config, now time.Time) dashboardModel {
	return dashboardModel{cfg: cfg, started: now, now: now, scale: scaleFloors, logs: viewport.New(0, 0)}
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
	d.scale.bitrate = max(d.scale.bitrate, niceCeil(s.Bitrate))
	d.scale.rtt = max(d.scale.rtt, niceCeil(s.RTT))
	d.scale.loss = max(d.scale.loss, niceCeil(s.Loss))
	return d.layout()
}

func (d dashboardModel) applyPoll(msg pollMsg) dashboardModel {
	d.now = msg.at
	d.linkRates = linkRates(d.prevGroups, msg.groups, msg.at.Sub(d.prevPoll))
	d.prevGroups, d.prevPoll = msg.groups, msg.at
	d.groups = msg.groups
	d.relay = msg.relay
	d.output = msg.output
	d = d.trackLinks()
	// In server mode the relay's publisher is the upstream leg; sample it the
	// same way the client does over the telemetry channel.
	if msg.relay != nil && msg.relay.Stats != nil {
		d = d.addSample(sampleFromStats(msg.at, msg.relay.Stats))
	}
	d = d.detectEvents()
	return d.layout()
}

// trackLinks records each link's rate history and first-seen time, forgetting
// links that are gone.
func (d dashboardModel) trackLinks() dashboardModel {
	hist, seen := map[string][]float64{}, map[string]time.Time{}
	for _, c := range d.conns() {
		h := d.linkHist[c.Addr]
		if r, ok := d.linkRates[c.Addr]; ok {
			h = append(h, r)
			if over := len(h) - linkHistory; over > 0 {
				h = slices.Clone(h[over:])
			}
			d.scale.link = max(d.scale.link, niceCeil(r))
		}
		hist[c.Addr] = h
		seen[c.Addr] = d.linkSeen[c.Addr]
		if seen[c.Addr].IsZero() {
			seen[c.Addr] = d.now
		}
	}
	d.linkHist, d.linkSeen = hist, seen
	return d
}

func (d dashboardModel) conns() []srtlaConnInfo {
	var conns []srtlaConnInfo
	for _, g := range d.groups {
		conns = append(conns, g.Conns...)
	}
	return conns
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

func (d dashboardModel) latest() (streamSample, bool) {
	if len(d.samples) == 0 {
		return streamSample{}, false
	}
	s := d.samples[len(d.samples)-1]
	return s, d.now.Sub(s.At) < staleAfter
}

// streamLive reports whether the upstream stream is currently arriving.
func (d dashboardModel) streamLive() bool {
	if d.cfg.normalizedMode() == "server" {
		return d.relay != nil && d.relay.PublisherAddr != ""
	}
	_, live := d.latest()
	return live
}

// lastInterval returns the packet counters of the latest statistics interval.
// ok is false until two samples of the same connection are available.
func (d dashboardModel) lastInterval() (recv, retrans, late uint64, ok bool) {
	n := len(d.samples)
	if n < 2 {
		return 0, 0, 0, false
	}
	a, b := d.samples[n-2], d.samples[n-1]
	if b.PktRecv < a.PktRecv || b.PktRetrans < a.PktRetrans || b.PktLate < a.PktLate {
		return 0, 0, 0, false // counters reset: the stream reconnected
	}
	return b.PktRecv - a.PktRecv, b.PktRetrans - a.PktRetrans, b.PktLate - a.PktLate, true
}

func (d dashboardModel) series(f func(streamSample) float64) []float64 {
	out := make([]float64, len(d.samples))
	for i, s := range d.samples {
		out[i] = f(s)
	}
	return out
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
	case loss > highLoss:
		return badStyle
	case loss > lowLoss:
		return warnStyle
	default:
		return goodStyle
	}
}

type outputState int

const (
	outputIdle outputState = iota
	outputSending
	outputFailing
)

// outputState classifies the UDP downstream output. UDP has no connection, so
// "sending" only means writes succeed; a refused write means nothing is
// listening on the port (e.g. the player is not running yet).
func (d dashboardModel) outputState() outputState {
	o := d.output
	switch {
	case !o.LastErr.IsZero() && d.now.Sub(o.LastErr) < udpOutputErrorHold:
		return outputFailing
	case !o.LastOK.IsZero() && d.now.Sub(o.LastOK) < staleAfter:
		return outputSending
	default:
		return outputIdle
	}
}

func (d dashboardModel) outputError() string {
	if strings.Contains(d.output.Err, "connection refused") {
		return "no listener"
	}
	return d.output.Err
}

func (d dashboardModel) outputStatus() string {
	switch d.outputState() {
	case outputFailing:
		return badStyle.Render("✗ "+d.outputError()) + dimStyle.Render("  (dropping packets)")
	case outputSending:
		return goodStyle.Render("● sending")
	default:
		return dimStyle.Render("○ idle")
	}
}

// ── Events ──────────────────────────────────────────────────────────────────

type dashEvent struct {
	At   time.Time
	Mark string // styled icon
	Text string
}

// watchState is what detectEvents compared against on the previous poll.
type watchState struct {
	live      bool
	lossSpike bool
	output    outputState
	stalled   map[string]bool // known links, and whether each is stalled
	clients   int
}

func (d dashboardModel) addEvent(style lipgloss.Style, icon, text string) dashboardModel {
	d.events = append(d.events, dashEvent{At: d.now, Mark: style.Render(icon), Text: text})
	if over := len(d.events) - maxEvents; over > 0 {
		d.events = append([]dashEvent(nil), d.events[over:]...)
	}
	return d
}

// detectEvents turns state changes since the previous poll into events, so
// what happened stays visible after the fact without digging through logs.
func (d dashboardModel) detectEvents() dashboardModel {
	w := d.watch

	if live := d.streamLive(); live != w.live {
		switch {
		case live && d.relay != nil:
			d = d.addEvent(goodStyle, "▶", "stream started from "+d.relay.PublisherAddr)
		case live:
			d = d.addEvent(goodStyle, "▶", "stream started")
		default:
			d = d.addEvent(warnStyle, "■", "stream lost")
		}
		w.live = live
	}

	if s, live := d.latest(); live {
		switch {
		case !w.lossSpike && s.Loss > highLoss:
			w.lossSpike = true
			d = d.addEvent(badStyle, "✗", fmt.Sprintf("loss spike %.1f %%", s.Loss))
		case w.lossSpike && s.Loss < lowLoss:
			w.lossSpike = false
			d = d.addEvent(goodStyle, "✓", fmt.Sprintf("loss back to %.1f %%", s.Loss))
		}
	}

	stalled := map[string]bool{}
	for _, c := range d.conns() {
		s := d.now.Sub(c.LastRcvd) > linkStallAfter
		stalled[c.Addr] = s
		was, known := w.stalled[c.Addr]
		switch {
		case !known:
			d = d.addEvent(goodStyle, "+", "link joined "+c.Addr)
		case s && !was:
			d = d.addEvent(warnStyle, "!", "link stalled "+c.Addr)
		case !s && was:
			d = d.addEvent(goodStyle, "✓", "link recovered "+c.Addr)
		}
	}
	var gone []string
	for addr := range w.stalled {
		if _, ok := stalled[addr]; !ok {
			gone = append(gone, addr)
		}
	}
	slices.Sort(gone)
	for _, addr := range gone {
		d = d.addEvent(warnStyle, "-", "link left "+addr)
	}
	w.stalled = stalled

	if st := d.outputState(); st != w.output {
		switch st {
		case outputFailing:
			d = d.addEvent(badStyle, "✗", "UDP downstream: "+d.outputError())
		case outputSending:
			d = d.addEvent(goodStyle, "✓", "UDP downstream sending")
		}
		w.output = st
	}

	if d.relay != nil && d.relay.Subscribers != w.clients {
		w.clients = d.relay.Subscribers
		d = d.addEvent(accentStyle, "•", fmt.Sprintf("stream clients: %d", w.clients))
	}

	d.watch = w
	return d
}

// ── Layout ──────────────────────────────────────────────────────────────────

type layoutTier int

const (
	tierNarrow layoutTier = iota // one column: stream, links, logs
	tierMedium                   // two columns
	tierWide                     // two columns with links beside endpoints
)

func (d dashboardModel) tier() layoutTier {
	switch {
	case d.width >= wideWidth:
		return tierWide
	case d.width >= mediumWidth:
		return tierMedium
	default:
		return tierNarrow
	}
}

func (d dashboardModel) viewWidth() int { return max(d.width, 40) }

// logsPanelWidth is the outer width of the Logs panel, which shares its row
// with Events except in the narrow layout.
func (d dashboardModel) logsPanelWidth() int {
	if d.tier() == tierNarrow {
		return d.viewWidth()
	}
	_, w := splitWidth(d.viewWidth(), 40)
	return w
}

// layout sizes the log viewport to fill the space below the panels, dropping
// the Endpoints panel when the terminal is too short for it.
func (d dashboardModel) layout() dashboardModel {
	if d.width == 0 {
		return d
	}
	atBottom := d.logs.AtBottom()
	free := func() int {
		return d.height - lipgloss.Height(d.header()) - lipgloss.Height(d.upper()) - 1 - 2 // footer, log border
	}
	d.hideEndpoints = false
	if d.tier() != tierNarrow && free() < 3 {
		d.hideEndpoints = true
	}
	d.logs.Width = d.logsPanelWidth() - 4
	d.logs.Height = max(free(), 3)
	if atBottom {
		d.logs.GotoBottom()
	}
	return d
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

// upper renders the panels between the header and the Events/Logs row.
func (d dashboardModel) upper() string {
	w := d.viewWidth()
	withLinks := d.cfg.normalizedMode() != "client"
	endpoints := boxTitleStyle.Render("Endpoints")
	var rows []string

	switch d.tier() {
	case tierNarrow:
		rows = append(rows, panel(boxTitleStyle.Render("Stream"), d.streamContent(w-4, true), w, 0))
		if withLinks {
			rows = append(rows, panel(d.linksTitle(), d.linksContent(w-4, true), w, 0))
		}

	case tierMedium:
		rows = append(rows, d.streamRow(splitWidth(w, 50)))
		if withLinks {
			rows = append(rows, panel(d.linksTitle(), d.linksContent(w-4, false), w, 0))
		}
		if !d.hideEndpoints {
			rows = append(rows, panel(endpoints, d.endpointsContent(w-4), w, 0))
		}

	default:
		lw, rw := splitWidth(w, 60)
		rows = append(rows, d.streamRow(lw, rw))
		switch {
		case withLinks && !d.hideEndpoints:
			links, eps := d.linksContent(lw-4, false), d.endpointsContent(rw-4)
			h := max(lipgloss.Height(links), lipgloss.Height(eps))
			rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top,
				panel(d.linksTitle(), links, lw, h), panel(endpoints, eps, rw, h)))
		case withLinks:
			rows = append(rows, panel(d.linksTitle(), d.linksContent(w-4, false), w, 0))
		case !d.hideEndpoints:
			rows = append(rows, panel(endpoints, d.endpointsContent(w-4), w, 0))
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// streamRow renders the bitrate chart beside the stream status, with the
// chart sized to the status panel's height.
func (d dashboardModel) streamRow(lw, rw int) string {
	status := d.streamContent(rw-4, false)
	h := lipgloss.Height(status)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		panel(boxTitleStyle.Render("Bitrate"), d.bitrateContent(lw-4, h), lw, h),
		panel(boxTitleStyle.Render("Stream"), status, rw, h))
}

// lower renders the Events and Logs panels.
func (d dashboardModel) lower() string {
	title := boxTitleStyle.Render("Logs")
	if !d.logs.AtBottom() && len(d.logLines) > 0 {
		title += dimStyle.Render(fmt.Sprintf(" ↑ %.0f%%", d.logs.ScrollPercent()*100))
	}
	h := d.logs.Height
	logs := panel(title, d.logs.View(), d.logsPanelWidth(), h)
	if d.tier() == tierNarrow {
		return logs
	}
	ew, _ := splitWidth(d.viewWidth(), 40)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		panel(boxTitleStyle.Render("Events"), d.eventsContent(h), ew, h), logs)
}

func (d dashboardModel) footer() string {
	hint := func(key, desc string) string { return accentStyle.Render(key) + " " + dimStyle.Render(desc) }
	f := strings.Join([]string{
		hint("q", "quit"),
		hint("↑↓ pgup/pgdn", "scroll logs"),
		hint("g/G", "top/bottom"),
	}, dimStyle.Render("  ·  "))
	return ansi.Truncate(f, d.viewWidth(), "…")
}

func (d dashboardModel) View() string {
	return lipgloss.JoinVertical(lipgloss.Left, d.header(), d.upper(), d.lower(), d.footer())
}

// ── Panels ──────────────────────────────────────────────────────────────────

const kvLabelWidth = 20

// kv renders a labelled row within width columns, moving the value to its own
// wrapped line when it does not fit beside the label.
func kv(width int, label, value string) string {
	if lipgloss.Width(value) <= width-kvLabelWidth {
		return dimStyle.Render(fmt.Sprintf("%-*s", kvLabelWidth, label)) + value
	}
	return dimStyle.Render(label) + "\n" + lipgloss.NewStyle().Width(max(width, 10)).Render(value)
}

func (d dashboardModel) endpointsContent(width int) string {
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
	return strings.Join(rows, "\n")
}

// bitrateContent renders the current bitrate with window statistics above an
// area chart of height-1 rows.
func (d dashboardModel) bitrateContent(width, height int) string {
	ceil := d.scale.bitrate
	label := fmt.Sprintf("%g", ceil)
	lw := len(label)
	cw := max(width-lw-1, 1)
	shown := d.samples[max(len(d.samples)-2*cw, 0):]

	head := dimStyle.Render("– Mbps")
	if s, live := d.latest(); live {
		head = valueStyle.Render(fmt.Sprintf("%.1f Mbps", s.Bitrate))
	}
	if len(shown) > 0 {
		lo, hi, sum := shown[0].Bitrate, shown[0].Bitrate, 0.0
		for _, s := range shown {
			lo, hi, sum = min(lo, s.Bitrate), max(hi, s.Bitrate), sum+s.Bitrate
		}
		span := shown[len(shown)-1].At.Sub(shown[0].At)
		head += dimStyle.Render(fmt.Sprintf("   avg %.1f · min %.1f · max %.1f · %s", sum/float64(len(shown)), lo, hi, shortDuration(span)))
	}

	values := make([]float64, len(shown))
	for i, s := range shown {
		values[i] = s.Bitrate
	}
	lines := []string{head}
	rows := brailleChart(values, cw, max(height-1, 1), ceil)
	for i, r := range rows {
		axis := strings.Repeat(" ", lw) + "│"
		switch i {
		case 0:
			axis = label + "┤"
		case len(rows) - 1:
			axis = fmt.Sprintf("%*s┤", lw, "0")
		}
		lines = append(lines, dimStyle.Render(axis)+sparkStyle.Render(r))
	}
	return strings.Join(lines, "\n")
}

const streamLabelWidth = 13

// streamContent renders the stream's status and health. In the narrow layout
// (withBitrate) it also carries the bitrate, as there is no chart panel.
func (d dashboardModel) streamContent(width int, withBitrate bool) string {
	row := func(label, value string) string {
		return dimStyle.Render(fmt.Sprintf("%-*s", streamLabelWidth, label)) + value
	}
	const valueWidth = 9
	chartWidth := min(width-streamLabelWidth-valueWidth-2, 30)
	// withChart pads value to a column and appends a one-row chart of history.
	withChart := func(value string, history []float64, ceil float64, style lipgloss.Style) string {
		if chartWidth < 4 {
			return value
		}
		pad := strings.Repeat(" ", max(valueWidth-lipgloss.Width(value), 0))
		return value + pad + "  " + style.Render(brailleChart(history, chartWidth, 1, ceil)[0])
	}
	none := dimStyle.Render("–")

	s, live := d.latest()
	var rows []string
	if d.streamLive() {
		rows = append(rows, row("Status", goodStyle.Render("● receiving")))
	} else {
		rows = append(rows, row("Status", dimStyle.Render("○ waiting for stream")))
	}

	if withBitrate {
		v := none
		if live {
			v = valueStyle.Render(fmt.Sprintf("%.1f Mbps", s.Bitrate))
		}
		if sw := width - streamLabelWidth - 11; sw >= 4 {
			v += strings.Repeat(" ", max(11-lipgloss.Width(v), 0)) + sparkStyle.Render(sparkline(d.series(func(s streamSample) float64 { return s.Bitrate }), min(sw, 30), d.scale.bitrate))
		}
		rows = append(rows, row("Bitrate", v))
	}

	rtt, loss, recovered, late, buffer := none, none, none, none, none
	if live {
		rtt = fmt.Sprintf("%.0f ms", s.RTT)
		loss = lossStyle(s.Loss).Render(fmt.Sprintf("%.1f %%", s.Loss))
		if recv, retrans, lateN, ok := d.lastInterval(); ok {
			// gosrt counts a packet as late when it arrives after its playout
			// time; a late retransmission did not recover anything.
			rec := retrans - min(lateN, retrans)
			pct := 0.0
			if recv > 0 {
				pct = float64(rec) / float64(recv) * 100
			}
			recovered = fmt.Sprintf("%.1f %%", pct) + dimStyle.Render(fmt.Sprintf("  %d pkts", rec))
			style := goodStyle
			if lateN > 0 {
				style = badStyle
			}
			late = style.Render(fmt.Sprintf("%d pkts", lateN)) + dimStyle.Render(fmt.Sprintf("  %d total", s.PktLate))
		}
		if s.LatencyMs > 0 {
			ratio := float64(s.BufferMs) / float64(s.LatencyMs)
			style := goodStyle
			switch {
			case ratio < 0.25:
				style = badStyle
			case ratio < 0.5:
				style = warnStyle
			}
			buffer = style.Render(gauge(ratio, 10)) + dimStyle.Render(fmt.Sprintf("  %.1fs / %.1fs", float64(s.BufferMs)/1000, float64(s.LatencyMs)/1000))
		}
	}
	lossChartStyle := sparkStyle
	if live {
		lossChartStyle = lossStyle(s.Loss)
	}
	rows = append(rows,
		row("RTT", withChart(rtt, d.series(func(s streamSample) float64 { return s.RTT }), d.scale.rtt, sparkStyle)),
		row("Loss", withChart(loss, d.series(func(s streamSample) float64 { return s.Loss }), d.scale.loss, lossChartStyle)),
		row(" ├ recovered", recovered),
		row(" └ too late", late),
		row("Buffer", buffer),
	)

	switch mode := d.cfg.normalizedMode(); {
	case mode != "server":
		rows = append(rows, row("UDP out", d.outputStatus()))
	case d.relay != nil:
		rows = append(rows, row("Clients", fmt.Sprintf("%d stream · %d stats", d.relay.Subscribers, d.relay.StatsClients)))
	}
	return strings.Join(rows, "\n")
}

func (d dashboardModel) linksTitle() string {
	conns := d.conns()
	total := 0.0
	for _, c := range conns {
		total += d.linkRates[c.Addr]
	}
	title := boxTitleStyle.Render("SRTLA links")
	if len(conns) > 0 {
		title += dimStyle.Render(fmt.Sprintf(" %d · %.1f Mbps", len(conns), total))
	}
	return title
}

// linksContent renders each SRTLA link with its share of the total. Optional
// columns are dropped from the right rather than wrapping a row.
func (d dashboardModel) linksContent(width int, compact bool) string {
	conns := d.conns()
	if len(conns) == 0 {
		return dimStyle.Render("no connections")
	}
	rates := make([]float64, len(conns))
	total := 0.0
	addrWidth := 0
	for i, c := range conns {
		rates[i] = d.linkRates[c.Addr]
		total += rates[i]
		addrWidth = max(addrWidth, len(c.Addr))
	}

	lines := []string{dimStyle.Render("share ") + shareBar(rates, width-6)}
	for i, c := range conns {
		rate, share := "     -    ", "   -"
		if r, ok := d.linkRates[c.Addr]; ok {
			rate = fmt.Sprintf("%5.1f Mbps", r)
			if total > 0 {
				share = fmt.Sprintf("%3.0f%%", r/total*100)
			}
		}
		row := linkMark(i) + " " + fmt.Sprintf("%-*s", addrWidth+2, c.Addr) + valueStyle.Render(rate) + "  " + dimStyle.Render(share)

		ago := d.now.Sub(c.LastRcvd)
		agoStr := goodStyle.Render(fmt.Sprintf("%4.1fs", ago.Seconds()))
		if ago > linkStallAfter {
			agoStr = warnStyle.Render(fmt.Sprintf("⚠ %.1fs", ago.Seconds()))
		}
		var extras []string
		if !compact {
			color := lipgloss.NewStyle().Foreground(linkColors[i%len(linkColors)])
			extras = append(extras, color.Render(sparkline(d.linkHist[c.Addr], 16, d.scale.link)))
		}
		extras = append(extras, agoStr, dimStyle.Render("up "+shortDuration(d.now.Sub(d.linkSeen[c.Addr]))))
		for _, e := range extras {
			if lipgloss.Width(row)+2+lipgloss.Width(e) > width {
				break
			}
			row += "  " + e
		}
		lines = append(lines, row)
	}
	return strings.Join(lines, "\n")
}

// eventsContent renders the most recent events that fit in height lines.
func (d dashboardModel) eventsContent(height int) string {
	if len(d.events) == 0 {
		return dimStyle.Render("no events yet")
	}
	events := d.events[max(len(d.events)-height, 0):]
	lines := make([]string, len(events))
	for i, e := range events {
		lines[i] = dimStyle.Render(e.At.Format("15:04:05")) + " " + e.Mark + " " + e.Text
	}
	return strings.Join(lines, "\n")
}
