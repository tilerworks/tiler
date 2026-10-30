package image

import (
	"errors"
	"os"
	"testing"
)

func TestCreationRejectsColonNamesBeforeSideEffects(t *testing.T) {
	for _, name := range []string{"alice/app:abcd", "alice/bad:name/../app"} {
		t.Run(name, func(t *testing.T) {
			cfg := buildConfig(t)
			src := sourceDir(t)
			runner := &fakeRunner{body: []byte("image")}
			builders := map[string]func() error{
				"full": func() error {
					_, err := BuildFull(FullRequest{Config: cfg, Name: name, SrcDir: src, Run: runner.run})
					return err
				},
				"split full": func() error {
					_, err := BuildFull(FullRequest{Config: cfg, Name: name, SrcDir: src, Run: runner.run, BlockSize: 4})
					return err
				},
				"incremental": func() error {
					_, err := BuildIncr(IncrRequest{Config: cfg, Name: name, Upper: src, Run: runner.run})
					return err
				},
				"dmg": func() error {
					_, err := BuildDMG(DMGRequest{Config: cfg, Name: name, SrcDir: src, Run: runner.run})
					return err
				},
				"link": func() error {
					return LinkExternal(cfg.ImagesDir, logsFor(cfg.ImagesDir), name, src, "test")
				},
			}
			for kind, build := range builders {
				t.Run(kind, func(t *testing.T) {
					if err := build(); !errors.Is(err, ErrInvalidName) {
						t.Fatalf("error = %v, want invalid name", err)
					}
					if len(runner.calls) != 0 {
						t.Fatalf("invalid name invoked tools: %v", runner.calls)
					}
					if _, err := os.Stat(cfg.ImagesDir); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("image library was touched: %v", err)
					}
				})
			}
		})
	}
}
