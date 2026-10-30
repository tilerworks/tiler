package cli

import (
	"strings"
	"unicode"
)

// displayCell keeps combining marks and common emoji sequences together when
// wrapping. Ambiguous-width text uses one cell, as in most terminal defaults.
type displayCell struct {
	text  string
	width int
}

func displayCells(text string) []displayCell {
	var cells []displayCell
	join, regional := false, false
	for _, r := range text {
		mark := unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) ||
			r == '\ufe0f' || r == '\ufe0e' || r >= 0x1f3fb && r <= 0x1f3ff ||
			r >= 0xe0020 && r <= 0xe007f
		flag := r >= 0x1f1e6 && r <= 0x1f1ff
		if len(cells) > 0 && (mark || join || r == '\u200d' || flag && regional) {
			last := &cells[len(cells)-1]
			last.text += string(r)
			if join || r == '\ufe0f' || r == '\u20e3' || flag {
				last.width = max(last.width, 2)
			}
		} else {
			width := 1
			if wideRune(r) || emojiRune(r) {
				width = 2
			}
			if mark || r == '\u200d' {
				width = 0
			}
			cells = append(cells, displayCell{string(r), width})
		}
		join = r == '\u200d'
		regional = flag && !regional
	}
	return cells
}

func wideRune(r rune) bool {
	return r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
		r >= 0x2e80 && r <= 0xa4cf && r != 0x303f ||
		r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff ||
		r >= 0xfe10 && r <= 0xfe19 || r >= 0xfe30 && r <= 0xfe6f ||
		r >= 0xff00 && r <= 0xff60 || r >= 0xffe0 && r <= 0xffe6 ||
		r >= 0x1f000 && r <= 0x1faff || r >= 0x20000 && r <= 0x3fffd)
}

// emojiRune covers default-wide emoji outside the supplementary emoji block.
func emojiRune(r rune) bool {
	switch {
	case r >= 0x231a && r <= 0x231b, r >= 0x23e9 && r <= 0x23ec,
		r == 0x23f0, r == 0x23f3, r >= 0x25fd && r <= 0x25fe,
		r >= 0x2614 && r <= 0x2615, r >= 0x2648 && r <= 0x2653,
		r == 0x267f, r == 0x2693, r == 0x26a1, r >= 0x26aa && r <= 0x26ab,
		r >= 0x26bd && r <= 0x26be, r >= 0x26c4 && r <= 0x26c5,
		r == 0x26ce, r == 0x26d4, r == 0x26ea, r >= 0x26f2 && r <= 0x26f3,
		r == 0x26f5, r == 0x26fa, r == 0x26fd, r == 0x2705,
		r >= 0x270a && r <= 0x270b, r == 0x2728, r == 0x274c, r == 0x274e,
		r >= 0x2753 && r <= 0x2755, r == 0x2757, r >= 0x2795 && r <= 0x2797,
		r == 0x27b0, r == 0x27bf, r >= 0x2b1b && r <= 0x2b1c,
		r == 0x2b50, r == 0x2b55:
		return true
	default:
		return false
	}
}

func displayWidth(text string) int {
	width := 0
	for _, cell := range displayCells(text) {
		width += cell.width
	}
	return width
}

// takeDisplay splits only at a display-cell boundary.
func takeDisplay(text string, width int) (string, string) {
	bytes, used := 0, 0
	for _, cell := range displayCells(text) {
		if used+cell.width > width {
			break
		}
		bytes += len(cell.text)
		used += cell.width
	}
	return text[:bytes], text[bytes:]
}

func truncateDisplay(text string, width int) string {
	if displayWidth(text) <= width {
		return text
	}
	prefix, _ := takeDisplay(text, max(0, width-1))
	return prefix + "…"
}

func wrapDescription(text string, width int) []string {
	first, rest := takeDisplay(text, width)
	if rest == "" {
		return []string{first}
	}
	// Prefer a word boundary when it leaves at least half of the first line.
	if split := strings.LastIndexByte(first, ' '); split >= 0 && displayWidth(first[:split]) >= width/2 {
		rest = first[split+1:] + rest
		first = first[:split]
	}
	return []string{strings.TrimRight(first, " "), truncateDisplay(strings.TrimSpace(rest), width)}
}
