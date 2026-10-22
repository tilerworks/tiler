package doctor

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
)

// probeTask is one environment check. Tasks in one batch run
// concurrently. The slice order is the report order.
type probeTask struct {
	name string
	run  func(context.Context) (Result, error)
}

// runProbeTasks starts every task and waits for all of them. The
// progress block has one "probing" heading, then one indented row
// per module. On a terminal that row is rewritten with spaces and
// done when the task finishes, so the report below it starts only
// after every row is marked. A diagnostic failure uses fail instead
// of done. A hard error, including a cancelled context, discards
// the partial set after the other tasks have stopped.
func runProbeTasks(ctx context.Context, view *probeView, tasks []probeTask) ([]Result, error) {
	if err := contextErr(ctx, nil); err != nil {
		return nil, err
	}
	results := make([]Result, len(tasks))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i, task := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			view.start(task.name)
			result, err := task.run(ctx)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				view.finish(task.name, err.Error())
				return
			}
			results[i] = result
			view.finish(task.name, probeProgressMark(result.Status))
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

// withAdvice inserts the cache comparison after the cache measurement
// and prints its progress lines. forceInfo keeps the darwin perf
// section advisory: a measurement problem must not fail doctor.
func withAdvice(view *probeView, results []Result, advice Result, forceInfo bool) ([]Result, error) {
	if forceInfo {
		advice.Status = statusInfo
	}
	view.start(advice.Name)
	view.finish(advice.Name, probeProgressMark(advice.Status))
	out := make([]Result, 0, len(results)+1)
	inserted := false
	for _, result := range results {
		out = append(out, result)
		if result.Name == namePerfCache {
			out = append(out, advice)
			inserted = true
		}
	}
	if !inserted {
		return nil, fmt.Errorf("doctor: %s has no %s result", nameCacheAdvice, namePerfCache)
	}
	return out, nil
}

// probeProgressMark is the word after a finished probe line. A
// passing, advisory, or informational result is done. A diagnostic
// failure is fail, so the line does not stay in the probing state.
func probeProgressMark(status string) string {
	if status == statusFail {
		return "fail"
	}
	return "done"
}

const (
	// probeProgressHeading is the subject of the live probe block.
	// It sits at column 0, like the report headings.
	probeProgressHeading = "probing"
	// probeProgressIndent matches the report row indent, so each
	// module row lines up with the config and check rows.
	probeProgressIndent = "  "
)

// probeView prints the probing heading once, then one row per probe.
// On a terminal the finished mark replaces that row in place.
// Anywhere else the finish mark is a second row, so a redirected log
// stays plain text.
type probeView struct {
	mu     sync.Mutex
	w      io.Writer
	live   bool
	lines  int
	index  map[string]int
	markAt int
}

// progressLive reports whether progress should rewrite lines. Tests
// replace it. The default treats a character device as a terminal.
var progressLive = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// probeNames is every progress label in this run, including the
// cache comparison that is printed after the batch. The shared list
// fixes the column where done starts.
func probeNames(tasks []probeTask) []string {
	names := make([]string, 0, len(tasks)+1)
	for _, task := range tasks {
		names = append(names, task.name)
	}
	return append(names, nameCacheAdvice)
}

func newProbeView(w io.Writer, names []string) *probeView {
	live := false
	if w != nil {
		live = progressLive(w)
	}
	markAt := 0
	for _, name := range names {
		if n := len(probeProgressPrefix(name)); n > markAt {
			markAt = n
		}
	}
	if markAt > 0 {
		markAt++
	}
	view := &probeView{w: w, live: live, index: map[string]int{}, markAt: markAt}
	if w != nil {
		_, _ = fmt.Fprintf(w, "%s\n\n", probeProgressHeading)
	}
	return view
}

func probeProgressPrefix(name string) string {
	return probeProgressIndent + name + " ..."
}

// finishedLine pads the probing prefix with spaces so mark starts in
// the same column for every name supplied to newProbeView.
func (v *probeView) finishedLine(name, mark string) string {
	prefix := probeProgressPrefix(name)
	width := 0
	if v != nil {
		width = v.markAt
	}
	if width <= len(prefix) {
		return prefix + " " + mark
	}
	return fmt.Sprintf("%-*s%s", width, prefix, mark)
}

func (v *probeView) start(name string) {
	if v == nil || v.w == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.live {
		v.index[name] = v.lines
		v.lines++
	}
	_, _ = fmt.Fprintf(v.w, "%s\n", probeProgressPrefix(name))
}

func (v *probeView) finish(name, mark string) {
	if v == nil || v.w == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	line := v.finishedLine(name, mark)
	if !v.live {
		_, _ = fmt.Fprintf(v.w, "%s\n", line)
		return
	}
	idx, ok := v.index[name]
	if !ok {
		_, _ = fmt.Fprintf(v.w, "%s\n", line)
		return
	}
	up := v.lines - idx
	_, _ = fmt.Fprintf(v.w, "\033[%dA\r\033[2K%s\033[%dB\r", up, line, up)
}

// close ends the probing section with a blank line so the following
// config section does not sit on the next row.
func (v *probeView) close() {
	if v == nil || v.w == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	_, _ = fmt.Fprintln(v.w)
}

func toolTask(seams Seams, spec toolSpec) probeTask {
	return probeTask{name: spec.name, run: func(ctx context.Context) (Result, error) {
		item, err := probeTool(ctx, seams, spec)
		if err != nil {
			return Result{}, err
		}
		return item.result, nil
	}}
}

func fileLimitTask(name, dir string, opts Options, seams Seams) probeTask {
	return probeTask{name: name, run: func(ctx context.Context) (Result, error) {
		if err := contextErr(ctx, nil); err != nil {
			return Result{}, err
		}
		return probeFileLimit(name, dir, opts, seams.TruncateProbe), nil
	}}
}

func perfTask(name, dir string, seams Seams, dest **PerfNumbers) probeTask {
	return probeTask{name: name, run: func(ctx context.Context) (Result, error) {
		if err := contextErr(ctx, nil); err != nil {
			return Result{}, err
		}
		result, numbers := measureDir(ctx, seams, name, dir)
		*dest = numbers
		return result, nil
	}}
}

func contribTasks(seams Seams, infoOnly bool) []probeTask {
	tasks := make([]probeTask, 0, len(contribTools))
	for _, tool := range contribTools {
		tasks = append(tasks, probeTask{
			name: tool.name,
			run: func(context.Context) (Result, error) {
				if infoOnly {
					return probeDarwinContribTool(seams, tool.name, tool.binary), nil
				}
				return probeContribTool(seams, tool.name, tool.binary), nil
			},
		})
	}
	return tasks
}

// asInfo forces a task's diagnostic result to info. Darwin uses it
// for storage measurements that must not fail the hot path.
func asInfo(task probeTask) probeTask {
	run := task.run
	task.run = func(ctx context.Context) (Result, error) {
		result, err := run(ctx)
		if err != nil {
			return Result{}, err
		}
		result.Status = statusInfo
		return result, nil
	}
	return task
}
