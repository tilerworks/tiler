package doctor

import (
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func withProgressSize(t *testing.T, size func(io.Writer) (int, int)) {
	t.Helper()
	previous := progressSize
	progressSize = size
	t.Cleanup(func() { progressSize = previous })
}

// assertProgressFrames checks physical row and column bounds rather than
// matching one particular sequence of task completion redraws.
func assertProgressFrames(t *testing.T, text string, width, height int) {
	t.Helper()
	rows := regexp.MustCompile("\x1b\\[(\\d+);1H([^\x1b]*)")
	for _, match := range rows.FindAllStringSubmatch(text, -1) {
		row, err := strconv.Atoi(match[1])
		if err != nil || row < 1 || row > height {
			t.Fatalf("row %q exceeds height %d", match[1], height)
		}
		if len(match[2]) >= width {
			t.Fatalf("row %q wraps at width %d", match[2], width)
		}
		for _, r := range match[2] {
			if r < ' ' || r > '~' {
				t.Fatalf("unsafe live row %q", match[2])
			}
		}
	}
	if strings.ContainsAny(text, "\r\n") {
		t.Fatalf("live output can scroll: %q", text)
	}
}

func TestProbeProgressFitsSmallTerminalsAndRestoresScreen(t *testing.T) {
	for _, geometry := range [][2]int{{80, 24}, {20, 4}, {1, 1}, {2, 2}} {
		t.Run(strconv.Itoa(geometry[0])+"x"+strconv.Itoa(geometry[1]), func(t *testing.T) {
			width, height := geometry[0], geometry[1]
			withProgressSize(t, func(io.Writer) (int, int) { return width, height })
			var buf strings.Builder
			names := []string{"git", "image_toolchain", "file_limit_images", "cache_advice"}
			view := newProbeView(&buf, names)
			t.Cleanup(view.close)
			for _, name := range names {
				view.start(name)
			}
			for _, name := range names {
				view.finish(name, "done")
			}
			view.finish("git", "error\n\033[2J\t界")
			view.close()
			view.close()
			text := buf.String()
			if !strings.HasPrefix(text, "\033[?1049h") || !strings.HasSuffix(text, "\033[?1049l") ||
				strings.Count(text, "\033[?1049l") != 1 {
				t.Fatalf("alternate screen lifecycle: %q", text)
			}
			assertProgressFrames(t, text, width, height)
			if len(view.rows) != len(names) {
				t.Fatalf("small terminal lost probe states: %v", view.rows)
			}
			if width == 80 {
				for _, name := range names {
					if !strings.Contains(text, view.finishedLine(name, "done")) {
						t.Fatalf("missing completed row %s", name)
					}
				}
			}
		})
	}
}

type progressRecorder struct {
	strings.Builder
	frames chan string
}

func (w *progressRecorder) WriteString(text string) (int, error) {
	select {
	case w.frames <- text:
	default:
	}
	return w.Builder.WriteString(text)
}

func TestProbeProgressRedrawsOnResizeWhileProbeIsIdle(t *testing.T) {
	var small atomic.Bool
	withProgressSize(t, func(io.Writer) (int, int) {
		if small.Load() {
			return 10, 3
		}
		return 80, 24
	})
	w := &progressRecorder{frames: make(chan string, 32)}
	view := newProbeView(w, []string{"git", "image_toolchain"})
	t.Cleanup(view.close)
	view.start("git")
	view.start("image_toolchain")
	view.finish("image_toolchain", "done")
	for len(w.frames) > 0 {
		<-w.frames
	}
	small.Store(true)
	select {
	case frame := <-w.frames:
		assertProgressFrames(t, frame, 10, 3)
	case <-time.After(3 * time.Second):
		t.Fatal("idle progress did not resize")
	}
	small.Store(false)
	select {
	case frame := <-w.frames:
		for _, name := range []string{"git", "image_toolchain"} {
			if !strings.Contains(frame, name) {
				t.Fatalf("expanded window did not recover %s: %q", name, frame)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("idle progress did not expand")
	}
	view.close()
}

func TestProbeProgressRestoresScreenAfterCancellation(t *testing.T) {
	withProgressSize(t, func(io.Writer) (int, int) { return 20, 3 })
	host := newDarwinHost(true, nil)
	ctx, cancel := context.WithCancel(context.Background())
	host.seams.Hdiutil = func() error {
		cancel()
		return ctx.Err()
	}
	var buf strings.Builder
	_, err := Probe(ctx, host.seams, Options{ImagesDir: "/images", CacheDir: "/cache", Progress: &buf})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled probe returned %v", err)
	}
	if !strings.HasSuffix(buf.String(), "\033[?1049l") {
		t.Fatal("cancellation did not restore the original screen")
	}
}

func TestProbeProgressRejectsNonTerminals(t *testing.T) {
	for _, path := range []string{os.DevNull, t.TempDir() + "/progress"} {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		width, height := readProgressSize(file)
		_ = file.Close()
		if width != 0 || height != 0 {
			t.Fatalf("non-terminal %s has size %dx%d", path, width, height)
		}
	}
	t.Setenv("TERM", "dumb")
	if width, height := progressSize(&strings.Builder{}); width != 0 || height != 0 {
		t.Fatal("dumb terminal enabled live progress")
	}
}
