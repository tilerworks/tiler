package workspace

import (
	"fmt"
	"io"
	"os"
)

// pruneProgressPrefix is the subject of every upper-prune line. The
// percent and the done mark each follow it by one space.
const pruneProgressPrefix = "pruning upper ..."

// pruneProgress prints upper-prune status. A nil receiver writes
// nothing, so a request with no Progress leaves existing output
// unchanged.
type pruneProgress struct {
	w     io.Writer
	live  bool
	total int
	done  int
	shown int
	open  bool
}

// pruneProgressLive reports whether progress should rewrite one
// line. Tests replace it. The default treats an *os.File character
// device as a terminal and every other writer as a log.
var pruneProgressLive = func(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// newPruneProgress returns nil when w is nil.
func newPruneProgress(w io.Writer) *pruneProgress {
	if w == nil {
		return nil
	}
	return &pruneProgress{w: w, live: pruneProgressLive(w)}
}

// setTotal is the upper entry count taken before the walk.
func (p *pruneProgress) setTotal(n int) {
	if p == nil || n < 0 {
		return
	}
	p.total = n
}

// start writes the line that precedes the lowercheck mount.
func (p *pruneProgress) start() {
	p.writeLine(pruneProgressPrefix)
}

// advance adds n decided entries and refreshes the percent only
// when the integer percent rises through 1..99. A jump prints the
// new percent once. 100 is never printed.
func (p *pruneProgress) advance(n int) {
	if p == nil || n <= 0 || p.total <= 0 {
		return
	}
	p.done += n
	if p.done > p.total {
		p.done = p.total
	}
	pct := p.done * 100 / p.total
	if pct < 1 || pct > 99 || pct <= p.shown {
		return
	}
	p.shown = pct
	p.writeLine(fmt.Sprintf("%s %d%%", pruneProgressPrefix, pct))
}

// succeed writes the done mark after the prune window succeeds.
func (p *pruneProgress) succeed() {
	if p == nil {
		return
	}
	p.writeLine(pruneProgressPrefix + " done")
	p.finishLine()
}

// abandon ends a live line without the done mark so a warning can
// follow. A non-terminal writer already closed the start line.
func (p *pruneProgress) abandon() {
	p.finishLine()
}

func (p *pruneProgress) writeLine(line string) {
	if p == nil {
		return
	}
	if !p.live {
		p.write(line + "\n")
		return
	}
	if p.open {
		p.write("\r" + line)
		return
	}
	p.write(line)
	p.open = true
}

func (p *pruneProgress) finishLine() {
	if p == nil || !p.live || !p.open {
		return
	}
	p.write("\n")
	p.open = false
}

// write always uses Writer.Write. A promoted WriteString would skip
// a wrapper that only records Write.
func (p *pruneProgress) write(s string) {
	_, _ = p.w.Write([]byte(s))
}
