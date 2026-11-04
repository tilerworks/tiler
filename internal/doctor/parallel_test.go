package doctor

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDarwinProbesOverlapAndReportProgress(t *testing.T) {
	host := newDarwinHost(true, nil)
	entered := make(chan string, 2)
	release := make(chan struct{})
	var closeOnce sync.Once
	unblock := func() { closeOnce.Do(func() { close(release) }) }
	note := func(name string) {
		entered <- name
		<-release
	}
	baseRun := host.seams.Run
	baseHdiutil := host.seams.Hdiutil
	host.seams.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.HasSuffix(name, "/git") {
			note("git")
		}
		return baseRun(ctx, name, args...)
	}
	host.seams.Hdiutil = func() error {
		note("image_toolchain")
		return baseHdiutil()
	}

	overlapped := make(chan struct{})
	go func() {
		defer unblock()
		seen := map[string]bool{}
		deadline := time.After(3 * time.Second)
		for len(seen) < 2 {
			select {
			case name := <-entered:
				seen[name] = true
			case <-deadline:
				return
			}
		}
		close(overlapped)
	}()

	var progress strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results, err := Probe(ctx, host.seams, Options{
		ImagesDir: "/images",
		CacheDir:  "/cache",
		Progress:  &progress,
	})
	unblock()
	select {
	case <-overlapped:
	default:
		t.Fatal("git and image_toolchain did not run at the same time")
	}
	if err != nil {
		t.Fatal(err)
	}
	assertDarwinProbeNames(t, results)
	text := progress.String()
	for _, name := range []string{"git", "image_toolchain", "cache_advice"} {
		line := probeProgressPrefix(name) + "\n"
		if !strings.HasPrefix(line, probeProgressIndent) || !strings.Contains(text, line) {
			t.Fatalf("progress missing %q\n%s", line, text)
		}
	}
	columns := map[string]int{}
	for _, line := range strings.Split(text, "\n") {
		for _, name := range []string{"git", "image_toolchain", "cache_advice"} {
			prefix := probeProgressPrefix(name)
			if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, "done") {
				continue
			}
			gap := line[len(prefix) : len(line)-len("done")]
			if gap == "" || strings.Trim(gap, " ") != "" {
				t.Fatalf("mark gap = %q, want spaces", gap)
			}
			columns[name] = strings.LastIndex(line, "done")
		}
	}
	if columns["git"] == 0 || columns["git"] != columns["image_toolchain"] || columns["git"] != columns["cache_advice"] {
		t.Fatalf("done columns = %#v\n%s", columns, text)
	}
	if strings.Count(text, probeProgressHeading+"\n") != 1 {
		t.Fatalf("probing heading = %d\n%s", strings.Count(text, probeProgressHeading+"\n"), text)
	}
	if !strings.HasSuffix(text, "\n\n") {
		t.Fatalf("probing section missing the trailing blank line\n%q", text)
	}
	if strings.Contains(text, "📋") || strings.Contains(text, "🔧") {
		t.Fatalf("progress contains the finished report\n%s", text)
	}
}
