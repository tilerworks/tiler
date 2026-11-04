package doctor

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// progressSize is a seam for terminal geometry. A failed query or a dumb
// terminal selects plain-text logging instead of cursor controls.
var progressSize = func(w io.Writer) (int, int) {
	if os.Getenv("TERM") == "dumb" {
		return 0, 0
	}
	return readProgressSize(w)
}

// probeView owns an alternate screen while probing. Repainting from saved
// state avoids relative cursor accounting across scrolling and resizing.
// Only the visible rows are drawn; all probe states survive a small window.
type probeView struct {
	mu      sync.Mutex
	w       io.Writer
	live    bool
	closed  bool
	markAt  int
	names   []string
	rows    map[string]string
	focus   int
	width   int
	height  int
	size    func(io.Writer) (int, int)
	stop    chan struct{}
	stopped chan struct{}
}

func newProbeView(w io.Writer, names []string) *probeView {
	v := &probeView{w: w, rows: map[string]string{}, size: progressSize}
	for _, name := range names {
		if n := len(probeProgressPrefix(name)) + 1; n > v.markAt {
			v.markAt = n
		}
	}
	if w == nil {
		return v
	}
	v.width, v.height = v.size(w)
	v.live = v.width > 0 && v.height > 0
	if !v.live {
		_, _ = fmt.Fprintf(w, "%s\n\n", probeProgressHeading)
		return v
	}
	_, _ = io.WriteString(w, "\033[?1049h")
	v.draw()
	v.stop = make(chan struct{})
	v.stopped = make(chan struct{})
	go v.watchSize()
	return v
}

func probeProgressPrefix(name string) string {
	return probeProgressIndent + name + " ..."
}

// finishedLine aligns the completion marks in both live and plain output.
func (v *probeView) finishedLine(name, mark string) string {
	return fmt.Sprintf("%-*s%s", v.markAt, probeProgressPrefix(name), mark)
}

func (v *probeView) start(name string) {
	v.update(name, probeProgressPrefix(name))
}

func (v *probeView) finish(name, mark string) {
	if v == nil {
		return
	}
	v.update(name, v.finishedLine(name, mark))
}

func (v *probeView) update(name, line string) {
	if v == nil || v.w == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return
	}
	if !v.live {
		_, _ = fmt.Fprintln(v.w, line)
		return
	}
	if _, ok := v.rows[name]; !ok {
		v.names = append(v.names, name)
	}
	v.rows[name] = line
	for i, label := range v.names {
		if label == name {
			v.focus = i
			break
		}
	}
	v.draw()
}

// draw uses absolute positions, never a newline on the bottom row, and
// leaves the last column empty to avoid autowrap. Live text is ASCII so
// byte clipping also bounds terminal cells, even for arbitrary errors.
func (v *probeView) draw() {
	width, height := v.size(v.w)
	if width <= 0 || height <= 0 {
		return
	}
	v.width, v.height = width, height
	var frame strings.Builder
	frame.WriteString("\033[H\033[2J")
	rowStart := 3
	if height < len(v.names)+2 {
		// Reclaim the decorative blank row when space is scarce.
		rowStart = 2
	}
	available := max(0, height-rowStart+1)
	first := 0
	if len(v.names) > available {
		first = max(0, v.focus-available+1)
	}
	last := min(len(v.names), first+available)
	heading := probeProgressHeading
	if last-first < len(v.names) {
		heading = fmt.Sprintf("probing (%d/%d shown)", last-first, len(v.names))
	}
	writeRow := func(row int, text string) {
		text = strings.Map(func(r rune) rune {
			if r < ' ' || r > '~' {
				return '?'
			}
			return r
		}, text)
		if len(text) >= width {
			text = text[:width-1]
		}
		_, _ = fmt.Fprintf(&frame, "\033[%d;1H%s", row, text)
	}
	writeRow(1, heading)
	for i := first; i < last; i++ {
		writeRow(i-first+rowStart, v.rows[v.names[i]])
	}
	_, _ = io.WriteString(v.w, frame.String())
}

// watchSize also redraws while a slow probe is running. The callback is
// captured per view so tests and concurrent runs do not share geometry.
func (v *probeView) watchSize() {
	defer close(v.stopped)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-v.stop:
			return
		case <-ticker.C:
			v.mu.Lock()
			width, height := v.size(v.w)
			if !v.closed && (width != v.width || height != v.height) {
				v.draw()
			}
			v.mu.Unlock()
		}
	}
}

// close restores the original screen and cursor before the final report.
// It joins the resize watcher so nothing can repaint after restoration.
func (v *probeView) close() {
	if v == nil || v.w == nil {
		return
	}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	v.closed = true
	if v.live {
		close(v.stop)
		_, _ = io.WriteString(v.w, "\033[?1049l")
	} else {
		_, _ = fmt.Fprintln(v.w)
	}
	v.mu.Unlock()
	if v.live {
		<-v.stopped
	}
}
