package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// panel draws a rounded box width columns wide with title set into the top
// border, btop style. Content lines are truncated to fit; with height > 0 the
// content is padded or cut to exactly height lines.
func panel(title, content string, width, height int) string {
	inner := max(width-4, 1)
	lines := strings.Split(content, "\n")
	if height > 0 {
		for len(lines) < height {
			lines = append(lines, "")
		}
		lines = lines[:height]
	}

	border := lipgloss.NewStyle().Foreground(colorBorder)
	title = " " + ansi.Truncate(title, max(width-6, 0), "…") + " "
	fill := max(width-3-lipgloss.Width(title), 0)

	var b strings.Builder
	b.WriteString(border.Render("╭─") + title + border.Render(strings.Repeat("─", fill)+"╮"))
	side := border.Render("│")
	for _, l := range lines {
		l = ansi.Truncate(l, inner, "…")
		b.WriteString("\n" + side + " " + l + strings.Repeat(" ", inner-lipgloss.Width(l)) + " " + side)
	}
	b.WriteString("\n" + border.Render("╰"+strings.Repeat("─", max(width-2, 0))+"╯"))
	return b.String()
}

// splitWidth divides width columns into a left part of pct percent and the rest.
func splitWidth(width, pct int) (int, int) {
	l := width * pct / 100
	return l, width - l
}

// niceCeil rounds v up to 1, 2 or 5 times a power of ten, so chart scales
// read as round numbers.
func niceCeil(v float64) float64 {
	if v <= 0 {
		return 0
	}
	exp := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 5, 10} {
		if m*exp >= v {
			return m * exp
		}
	}
	return 10 * exp
}

// sparkline renders the last width values as bars scaled from 0 to ceil, so
// the same value always has the same height. Missing history is left blank.
func sparkline(values []float64, width int, ceil float64) string {
	levels := []rune("▁▂▃▄▅▆▇█")
	if len(values) > width {
		values = values[len(values)-width:]
	}
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", width-len(values)))
	for _, v := range values {
		i := 0
		if ceil > 0 {
			i = int(math.Ceil(v/ceil*float64(len(levels)))) - 1
		}
		b.WriteRune(levels[min(max(i, 0), len(levels)-1)])
	}
	return b.String()
}

// brailleChart renders the last 2×width values as a filled area chart of
// height rows scaled from 0 to ceil. Each braille cell holds two values and
// four dot rows, doubling the resolution of block characters. The newest value
// is at the right edge.
func brailleChart(values []float64, width, height int, ceil float64) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	n := width * 2
	if len(values) > n {
		values = values[len(values)-n:]
	}
	offset := n - len(values)
	levels := height * 4
	level := func(col int) int {
		i := col - offset
		if i < 0 || ceil <= 0 || values[i] <= 0 {
			return 0
		}
		// Anything above zero gets at least one dot.
		return min(max(int(math.Round(values[i]/ceil*float64(levels))), 1), levels)
	}

	// Dot bits of each braille column, bottom to top.
	dots := [2][4]rune{{0x40, 0x04, 0x02, 0x01}, {0x80, 0x20, 0x10, 0x08}}
	rows := make([]string, height)
	for r := range rows {
		below := (height - 1 - r) * 4 // dot rows under this chart row
		var b strings.Builder
		for c := 0; c < width; c++ {
			cell := rune(0x2800)
			for k := range 2 {
				fill := min(max(level(2*c+k)-below, 0), 4)
				for j := range fill {
					cell |= dots[k][j]
				}
			}
			b.WriteRune(cell)
		}
		rows[r] = b.String()
	}
	return rows
}

// gauge renders ratio (0–1) as a bar of width segments.
func gauge(ratio float64, width int) string {
	filled := min(max(int(math.Round(ratio*float64(width))), 0), width)
	return strings.Repeat("▰", filled) + strings.Repeat("▱", width-filled)
}

// Colors and glyphs telling SRTLA links apart; the glyphs keep links
// distinguishable without color.
var (
	linkColors = []lipgloss.AdaptiveColor{
		{Light: "#C62828", Dark: "#EF5350"},
		{Light: "#B26A00", Dark: "#FFB74D"},
		{Light: "#6A1B9A", Dark: "#BA68C8"},
		{Light: "#01579B", Dark: "#4FC3F7"},
	}
	linkGlyphs = []string{"█", "▓", "▒", "░"}
)

func linkMark(i int) string {
	return lipgloss.NewStyle().Foreground(linkColors[i%len(linkColors)]).Render(linkGlyphs[i%len(linkGlyphs)])
}

// shareBar renders a width-column bar split between links in proportion to
// their rates. Columns are handed out by largest remainder so the segments
// always fill the bar exactly.
func shareBar(rates []float64, width int) string {
	total := 0.0
	for _, r := range rates {
		total += max(r, 0)
	}
	if total <= 0 || width <= 0 {
		return dimStyle.Render(strings.Repeat("─", max(width, 0)))
	}
	cols := make([]int, len(rates))
	rem := make([]float64, len(rates))
	used := 0
	for i, r := range rates {
		exact := max(r, 0) / total * float64(width)
		cols[i] = int(exact)
		rem[i] = exact - float64(cols[i])
		used += cols[i]
	}
	for ; used < width; used++ {
		best := 0
		for i := range rem {
			if rem[i] > rem[best] {
				best = i
			}
		}
		cols[best]++
		rem[best] = -1
	}
	var b strings.Builder
	for i, c := range cols {
		b.WriteString(lipgloss.NewStyle().Foreground(linkColors[i%len(linkColors)]).Render(strings.Repeat(linkGlyphs[i%len(linkGlyphs)], c)))
	}
	return b.String()
}

// shortDuration formats d compactly: 45s, 42m, 1h03m.
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
