package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/doctor"
	"github.com/jiangxin/tiler/internal/image"
)

func TestImageListOutputWidthFlag(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	node := versionNode("a", image.KindFull, "", time.Now())
	node.Description = strings.Repeat("x", 400)
	writeVersionForest(t, imagesDir, "alice/app", node)
	for _, tc := range []struct {
		width int
		args  []string
	}{
		{120, nil},
		{80, []string{"-w", "80"}},
		{160, []string{"--width", "160"}},
	} {
		t.Run(fmt.Sprint(tc.width), func(t *testing.T) {
			args := append(append([]string{}, lib...), "image", "ls", "-v")
			out, stderr, _, err := runCmd(t, append(args, tc.args...)...)
			if err != nil {
				t.Fatalf("list: %v\n%s", err, stderr)
			}
			assertOutputWidth(t, out, tc.width)
			if len(strings.Split(strings.TrimSuffix(out, "\n"), "\n")) != 4 {
				t.Fatalf("expected two description lines: %s", out)
			}
		})
	}
	var baseline string
	for _, width := range []string{"80", "120", "160"} {
		args := append(append([]string{}, lib...), "images", "list", "--json", "--width", width)
		out, stderr, _, err := runCmd(t, args...)
		if err != nil {
			t.Fatalf("JSON: %v\n%s", err, stderr)
		}
		if baseline != "" && out != baseline {
			t.Fatal("width changed JSON output")
		}
		baseline = out
	}
}

func TestDoctorOutputWidthFlag(t *testing.T) {
	withWorkspace(t)
	restoreDoctor(t, func(context.Context, config.Config, io.Writer) ([]doctor.Result, error) {
		return []doctor.Result{{Name: "git", Status: "info", Detail: strings.Repeat("x", 400)}}, nil
	})
	for _, tc := range []struct {
		width int
		args  []string
	}{
		{120, nil},
		{80, []string{"-w", "80"}},
		{160, []string{"--width", "160"}},
	} {
		out, stderr, err := runDoctorCmd(context.Background(), t, tc.args...)
		if err != nil {
			t.Fatalf("doctor: %v\n%s", err, stderr)
		}
		assertOutputWidth(t, out, tc.width)
	}
	var baseline string
	for _, width := range []string{"80", "120", "160"} {
		out, stderr, err := runDoctorCmd(context.Background(), t, "--json", "--width", width)
		if err != nil {
			t.Fatalf("JSON: %v\n%s", err, stderr)
		}
		if baseline != "" && out != baseline {
			t.Fatal("width changed JSON output")
		}
		baseline = out
	}
}

func TestOutputWidthRejectsInvalidValuesBeforeProbing(t *testing.T) {
	withWorkspace(t)
	restoreDoctor(t, func(context.Context, config.Config, io.Writer) ([]doctor.Result, error) {
		t.Fatal("invalid width started probes")
		return nil, nil
	})
	for _, command := range [][]string{{"images", "ls"}, {"doctor"}} {
		for _, width := range []string{"-1", "0", "79", "bad", "999999999999999999999999"} {
			args := append(append([]string{}, command...), "--width", width)
			_, _, _, err := runCmd(t, args...)
			code := ExitCode(err)
			if err == nil || code != 2 || !strings.Contains(err.Error(), "width") {
				t.Fatalf("%v: code %d, error %v", args, code, err)
			}
		}
	}
}

func assertOutputWidth(t *testing.T, out string, width int) {
	t.Helper()
	widest := 0
	for _, line := range strings.Split(out, "\n") {
		got := displayWidth(line)
		if got > width {
			t.Fatalf("line has %d columns, limit %d: %q", got, width, line)
		}
		widest = max(widest, got)
	}
	if widest != width {
		t.Fatalf("width flag did not reach layout: widest line %d, expected %d", widest, width)
	}
}
