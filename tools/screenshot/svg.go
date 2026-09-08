package main

import (
	"fmt"
	"os"
	"strings"
)

// Terminal window metrics, in SVG user units.
const (
	cellW   = 8.4
	cellH   = 18.0
	padX    = 22.0
	padTop  = 40.0 // room for the title bar
	padBot  = 16.0
	radius  = 10.0
	fontPx  = 14.0
	titleH  = 28.0
	bgColor = "#14141f"
	fgColor = "#e8e8e8"

	// A colour emoji face has to be in the stack or country flags — pairs of
	// regional indicator codepoints — render as tofu boxes in the output.
	// The monospace faces come first so ordinary text is unaffected.
	fontFamily = "JetBrainsMono Nerd Font, JetBrains Mono, monospace, " +
		"Noto Color Emoji, Apple Color Emoji, Segoe UI Emoji"
)

// Every screenshot is drawn in the same frame, whatever the screen has to
// say, so the four windows in the README grid are the same size instead of
// one being visibly taller than its neighbour. frameRows is the tallest
// screen (conflicts) with a little headroom; frameCols matches the width the
// models are rendered at.
const (
	frameCols = 92
	frameRows = 32
)

// vAlign says where a screen sits inside the fixed frame.
type vAlign int

const (
	// alignTop is for screens under the tab bar. The TUI centres itself
	// vertically in a real terminal, but centring here puts the tab bar at a
	// different height in every window, and four windows side by side then
	// read as four different programs.
	alignTop vAlign = iota
	// alignMiddle is for screens with no tab bar above them — the login
	// form — which have no chrome to line up with and look dropped if they
	// are pinned to the top of an otherwise empty window.
	alignMiddle
)

// fitFrame trims a screen down to its content and then places it in the
// fixed frame according to align.
func fitFrame(grid [][]cell, align vAlign) [][]cell {
	grid = trimBlankEdges(grid)
	// One blank row top and bottom, so content is never flush to the frame.
	grid = append([][]cell{nil}, append(grid, nil)...)

	if len(grid) > frameRows {
		fmt.Fprintf(os.Stderr,
			"screenshot: a screen needs %d rows but frameRows is %d; "+
				"raise it, or this window will be taller than the others\n",
			len(grid), frameRows)
		return grid
	}

	top := 0
	if align == alignMiddle {
		top = (frameRows - len(grid)) / 2
	}
	out := make([][]cell, frameRows)
	copy(out[top:], grid)
	return out
}

// trimBlankEdges drops fully blank rows from the top and bottom.
//
// Screens are centred inside the terminal height, so rendering one at a
// generous size leaves large empty margins. Trimming lets each screenshot fit
// its own content without hand-tuning a height per screen.
func trimBlankEdges(grid [][]cell) [][]cell {
	blank := func(line []cell) bool {
		return len(trimTrailing(line)) == 0
	}
	start := 0
	for start < len(grid) && blank(grid[start]) {
		start++
	}
	end := len(grid)
	for end > start && blank(grid[end-1]) {
		end--
	}
	grid = grid[start:end]

	// Screens are also centred vertically, which leaves a run of blank rows
	// between the tab bar and the panel. Collapse any run to one. Rows inside
	// a panel carry its border characters, so they are never blank and are
	// never touched.
	out := make([][]cell, 0, len(grid))
	prevBlank := false
	for _, line := range grid {
		isBlank := blank(line)
		if isBlank && prevBlank {
			continue
		}
		out = append(out, line)
		prevBlank = isBlank
	}
	return out
}

// renderSVG draws a terminal window containing the given screen.
func renderSVG(title string, grid [][]cell, align vAlign) string {
	grid = fitFrame(grid, align)

	cols := frameCols
	for _, line := range grid {
		if n := len(line); n > cols {
			cols = n
		}
	}

	w := float64(cols)*cellW + 2*padX
	h := float64(len(grid))*cellH + padTop + padBot

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f" `+
		`viewBox="0 0 %.0f %.0f" font-family="%s" `+
		"font-size=\"%.1f\">\n", w, h, w, h, fontFamily, fontPx)

	// Window frame.
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%.0f" height="%.0f" rx="%.0f" fill="%s"/>`+"\n",
		w, h, radius, bgColor)
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%.0f" height="%.0f" rx="%.0f" fill="#1d1d2b"/>`+"\n",
		w, titleH, radius)
	fmt.Fprintf(&b, `<rect x="0" y="%.0f" width="%.0f" height="%.0f" fill="#1d1d2b"/>`+"\n",
		radius, w, titleH-radius)
	for i, col := range []string{"#ff5f57", "#febc2e", "#28c840"} {
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="5" fill="%s"/>`+"\n",
			18+float64(i)*18, titleH/2, col)
	}
	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" fill="#8a8a9a" font-size="12" `+
		`text-anchor="middle">%s</text>`+"\n", w/2, titleH/2+4, escape(title))

	// Background runs first, so glyphs are never clipped by a later cell.
	for row, line := range grid {
		y := padTop + float64(row)*cellH
		for col, c := range line {
			if c.bg == "" {
				continue
			}
			fmt.Fprintf(&b, `<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" fill="%s"/>`+"\n",
				padX+float64(col)*cellW, y-cellH+5, cellW+0.5, cellH, c.bg)
		}
	}

	// Then glyphs, batching consecutive cells that share a style into one
	// text element — an SVG with one element per character is enormous.
	for row, line := range grid {
		line = trimTrailing(line)
		if len(line) == 0 {
			continue
		}
		y := padTop + float64(row)*cellH

		start := 0
		for i := 1; i <= len(line); i++ {
			if i < len(line) && sameStyle(line[i], line[start]) {
				continue
			}
			run := line[start:i]
			text := strings.TrimRight(string(runes(run)), " ")
			if text != "" {
				fmt.Fprintf(&b, `<text x="%.2f" y="%.2f" fill="%s"%s xml:space="preserve">%s</text>`+"\n",
					padX+float64(start)*cellW, y, colorOf(run[0]), weightOf(run[0]), escape(text))
			}
			start = i
		}
	}

	b.WriteString("</svg>\n")
	return b.String()
}

func sameStyle(a, b cell) bool {
	return a.fg == b.fg && a.bg == b.bg && a.bold == b.bold
}

func runes(cs []cell) []rune {
	out := make([]rune, len(cs))
	for i, c := range cs {
		out[i] = c.r
	}
	return out
}

func colorOf(c cell) string {
	if c.fg == "" {
		return fgColor
	}
	return c.fg
}

func weightOf(c cell) string {
	if c.bold {
		return ` font-weight="bold"`
	}
	return ""
}

func escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
