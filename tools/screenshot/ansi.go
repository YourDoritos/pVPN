package main

import (
	"fmt"
	"strconv"
	"strings"
)

// cell is one character with the styling in effect when it was written.
type cell struct {
	r    rune
	fg   string // "#rrggbb", empty for the default foreground
	bg   string
	bold bool
}

// parseANSI turns a rendered lipgloss screen into a grid of styled cells.
//
// Only what lipgloss actually emits is handled: 24-bit SGR colours, bold, and
// reset. Anything else is skipped rather than guessed at, so an unsupported
// sequence loses styling instead of corrupting the layout.
func parseANSI(s string) [][]cell {
	var (
		out  [][]cell
		line []cell
		cur  cell
	)

	for i := 0; i < len(s); {
		switch {
		case s[i] == '\n':
			out = append(out, line)
			line = nil
			i++

		case s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[':
			end := i + 2
			for end < len(s) && s[end] != 'm' && s[end] != 'K' && s[end] != 'H' {
				end++
			}
			if end >= len(s) {
				i = len(s)
				break
			}
			if s[end] == 'm' {
				applySGR(&cur, s[i+2:end])
			}
			i = end + 1

		default:
			r, size := decodeRune(s[i:])
			c := cur
			c.r = r
			line = append(line, c)
			i += size
		}
	}
	if len(line) > 0 {
		out = append(out, line)
	}
	return out
}

func decodeRune(s string) (rune, int) {
	for i, r := range s {
		if i == 0 {
			size := 1
			for size < len(s) && s[size]&0xC0 == 0x80 {
				size++
			}
			return r, size
		}
	}
	return ' ', 1
}

// applySGR interprets one SGR parameter list.
func applySGR(c *cell, params string) {
	if params == "" || params == "0" {
		*c = cell{}
		return
	}

	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		switch parts[i] {
		case "0":
			*c = cell{}
		case "1":
			c.bold = true
		case "22":
			c.bold = false
		case "38", "48":
			// 24-bit colour: 38;2;R;G;B
			if i+4 < len(parts) && parts[i+1] == "2" {
				r, _ := strconv.Atoi(parts[i+2])
				g, _ := strconv.Atoi(parts[i+3])
				b, _ := strconv.Atoi(parts[i+4])
				hex := fmt.Sprintf("#%02x%02x%02x", r, g, b)
				if parts[i] == "38" {
					c.fg = hex
				} else {
					c.bg = hex
				}
				i += 4
			}
		case "39":
			c.fg = ""
		case "49":
			c.bg = ""
		}
	}
}

// trimTrailing drops trailing blank cells so the SVG carries no invisible
// runs of spaces.
func trimTrailing(line []cell) []cell {
	end := len(line)
	for end > 0 && line[end-1].r == ' ' && line[end-1].bg == "" {
		end--
	}
	return line[:end]
}
