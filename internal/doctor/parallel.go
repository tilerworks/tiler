package doctor

import (
	"context"
	"fmt"
	"sync"
)

// probeTask is one environment check. Tasks in one batch run
// concurrently. The slice order is the report order.
type probeTask struct {
	name string
	run  func(context.Context) (Result, error)
}

// runProbeTasks starts every task and waits for all of them. The
// progress view has one "probing" heading, then one indented row
// per module. A terminal repaints the visible rows as tasks finish.
// A diagnostic failure uses fail instead of done. A hard error,
// including a cancelled context, discards the partial set after the
// other tasks have stopped.
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
	if err := ctx.Err(); err != nil {
		return nil, err
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
