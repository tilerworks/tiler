package benchmark

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"
)

const (
	volPath = "/tiler-bench-plan/vol"
	rtPath  = "/tiler-bench-plan/runtime"
)

func TestPlanWritableBothCreatesReadsAndProbes(t *testing.T) {
	got, err := Plan(Options{
		Path:     volPath,
		Type:     "tar",
		Size:     "128M",
		FileSize: "4M",
		Keep:     true,
		Format:   FormatTSV,
	}, writableVol("linux"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !got.Create || !got.Read || !got.Probe {
		t.Fatalf("create/read/probe = %v %v %v", got.Create, got.Read, got.Probe)
	}
	if got.Mode != "both" || got.Size != 128<<20 || got.FileSize != 4<<20 {
		t.Fatalf("mode/size/file-size = %s %d %d", got.Mode, got.Size, got.FileSize)
	}
	if !got.Keep || got.Format != FormatTSV || !got.OwnRuntime || got.RuntimeDir != "" {
		t.Fatalf("placement = %+v", got)
	}
	if got.BenchDir != volPath+"/tiler-bench" || got.Src != "data" {
		t.Fatalf("bench/src = %q %q, want %q %q",
			got.BenchDir, got.Src, volPath+"/tiler-bench", "data")
	}
	if got.Type != "tar" || got.Compress != "" || got.ProbeOps != 2000 {
		t.Fatalf("type/compress/ops = %s %q %d", got.Type, got.Compress, got.ProbeOps)
	}
}

func TestPlanReadonlyImageReadsOnly(t *testing.T) {
	got, err := Plan(Options{
		Path:     volPath,
		ReadOnly: true,
		Image:    "/tiler-bench-plan/app.tar",
		NoProbe:  true,
		Mode:     "read",
		Type:     "tar",
		Size:     "1",
		FileSize: "1",
	}, memFS{nodes: map[string]node{
		volPath: {dir: true, writable: false},
	}}.FS("linux"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.Create || !got.Read || got.Probe || got.BenchDir != "" || got.Src != "" {
		t.Fatalf("decision = %+v", got)
	}
	if got.Image != "/tiler-bench-plan/app.tar" || !got.ReadOnly {
		t.Fatalf("image = %+v", got)
	}
}

func TestPlanReadWithoutImageCreatesFirst(t *testing.T) {
	got, err := Plan(Options{
		Path:     volPath,
		Mode:     "read",
		Type:     "tar",
		Size:     "4K",
		FileSize: "4K",
	}, writableVol("linux"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !got.Create || !got.Read || !got.Probe {
		t.Fatalf("create/read/probe = %v %v %v", got.Create, got.Read, got.Probe)
	}
}

func TestPlanReadImageSkipsCreateAndProbe(t *testing.T) {
	got, err := Plan(Options{
		Path:     volPath,
		Mode:     "read",
		Image:    "/tiler-bench-plan/app.sqsh",
		NoProbe:  true,
		Type:     "tar",
		Size:     "1",
		FileSize: "1",
	}, writableVol("linux"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.Create || !got.Read || got.Probe {
		t.Fatalf("create/read/probe = %v %v %v", got.Create, got.Read, got.Probe)
	}
}

func TestPlanAllowsFileSizeAboveSize(t *testing.T) {
	got, err := Plan(Options{
		Path:     volPath,
		Type:     "tar",
		Size:     "1K",
		FileSize: "4K",
	}, writableVol("linux"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.Size != 1024 || got.FileSize != 4096 {
		t.Fatalf("size/file-size = %d %d", got.Size, got.FileSize)
	}
}

func TestPlanPlacesBenchDirAndRuntimeDir(t *testing.T) {
	sys := memFS{nodes: map[string]node{
		volPath:           {dir: true, writable: true},
		volPath + "/work": {dir: true, writable: true},
		rtPath:            {dir: true, writable: true},
	}}
	got, err := Plan(Options{
		Path:        volPath,
		BenchDir:    "work",
		BenchDirSet: true,
		RuntimeDir:  rtPath,
		Src:         "payload",
		SrcSet:      true,
		Type:        "tar",
		Size:        "128M",
		FileSize:    "4M",
	}, sys.FS("linux"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.BenchDir != volPath+"/work" || got.OwnRuntime {
		t.Fatalf("dirs = bench %s owned %v", got.BenchDir, got.OwnRuntime)
	}
	if got.RuntimeDir != rtPath+"/runtime" || got.RuntimeParent != rtPath ||
		got.RuntimeDirCreated {
		t.Fatalf("runtime = root %s parent %s created %v",
			got.RuntimeDir, got.RuntimeParent, got.RuntimeDirCreated)
	}
	if got.Src != "payload" || !got.Create {
		t.Fatalf("src = %+v", got)
	}
}

func TestPlanAcceptsMissingPath(t *testing.T) {
	got, err := Plan(Options{
		Path:     volPath,
		Type:     "tar",
		Size:     "1",
		FileSize: "1",
	}, memFS{}.FS("linux"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !got.CreatePath || got.Path != volPath {
		t.Fatalf("path = %s create %v", got.Path, got.CreatePath)
	}
	if got.BenchDir != volPath+"/tiler-bench" {
		t.Fatalf("bench = %s", got.BenchDir)
	}
}

func TestPlanCreatesMissingRuntimeDir(t *testing.T) {
	got, err := Plan(Options{
		Path:       volPath,
		RuntimeDir: rtPath,
		Type:       "tar",
		Size:       "1",
		FileSize:   "1",
	}, writableVol("linux"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.RuntimeDir != rtPath+"/runtime" || got.RuntimeParent != rtPath {
		t.Fatalf("runtime = root %s parent %s", got.RuntimeDir, got.RuntimeParent)
	}
	if !got.RuntimeDirCreated || got.OwnRuntime {
		t.Fatalf("created = %v owned = %v", got.RuntimeDirCreated, got.OwnRuntime)
	}
}

func TestPlanAcceptsNonEmptyRuntimeDir(t *testing.T) {
	got, err := Plan(Options{
		Path:       volPath,
		RuntimeDir: rtPath,
		Type:       "tar",
		Size:       "1",
		FileSize:   "1",
	}, memFS{nodes: map[string]node{
		volPath: {dir: true, writable: true},
		rtPath:  {dir: true, names: []string{"marker"}},
	}}.FS("linux"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.RuntimeDir != rtPath+"/runtime" || got.RuntimeDirCreated {
		t.Fatalf("runtime = root %s created %v", got.RuntimeDir, got.RuntimeDirCreated)
	}
}

func TestPlanAcceptsMissingBenchDir(t *testing.T) {
	got, err := Plan(Options{
		Path:        volPath,
		BenchDir:    "fresh",
		BenchDirSet: true,
		Type:        "tar",
		Size:        "1",
		FileSize:    "1",
	}, writableVol("linux"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.BenchDir != volPath+"/fresh" {
		t.Fatalf("bench = %s", got.BenchDir)
	}
}

func TestPlanReadImageIgnoresDefaultDirs(t *testing.T) {
	got, err := Plan(Options{
		Path:     volPath,
		Mode:     "read",
		Image:    "/tiler-bench-plan/app.sqsh",
		NoProbe:  true,
		Type:     "tar",
		Size:     "1",
		FileSize: "1",
	}, writableVol("linux"))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.BenchDir != "" || got.Src != "" {
		t.Fatalf("bench/src = %q %q, want empty defaults dropped", got.BenchDir, got.Src)
	}
}

func TestPlanResolvesCompression(t *testing.T) {
	cases := []struct {
		goos     string
		typ      string
		compress string
		wantType string
		wantComp string
	}{
		{goos: "darwin", typ: "dmg", wantType: "dmg", wantComp: "ulfo"},
		{goos: "darwin", typ: "dmg", compress: "ULMO", wantType: "dmg", wantComp: "ulmo"},
		{goos: "darwin", compress: "UdRo", wantType: "dmg", wantComp: "udro"},
		{goos: "linux", typ: "squashfs", wantType: "squashfs", wantComp: "zstd"},
		{goos: "linux", typ: "squashfs", compress: "lz4", wantType: "squashfs", wantComp: "lz4"},
		{goos: "linux", typ: "erofs", wantType: "erofs"},
		{goos: "linux", typ: "erofs", compress: "gzip", wantType: "erofs", wantComp: "gzip"},
		{goos: "linux", typ: "tar", wantType: "tar"},
	}
	for _, tc := range cases {
		name := tc.goos + "/" + tc.wantType + "/" + tc.compress
		t.Run(name, func(t *testing.T) {
			got, err := Plan(Options{
				Path:     volPath,
				Type:     tc.typ,
				Compress: tc.compress,
				Size:     "1",
				FileSize: "1",
			}, writableVol(tc.goos))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if got.Type != tc.wantType || got.Compress != tc.wantComp {
				t.Fatalf("type/compress = %s %q, want %s %q",
					got.Type, got.Compress, tc.wantType, tc.wantComp)
			}
		})
	}
}

func TestPlanUsageRefusals(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Options)
		fs   FS
		want string
	}{
		{
			name: "readonly without image",
			edit: func(o *Options) {
				o.ReadOnly = true
				o.NoProbe = true
				o.Mode = "read"
			},
			want: "--image",
		},
		{
			name: "readonly with probe",
			edit: func(o *Options) {
				o.ReadOnly = true
				o.Image = "/tiler-bench-plan/app.tar"
				o.Mode = "read"
			},
			want: "--no-probe",
		},
		{
			name: "readonly with bench-dir",
			edit: func(o *Options) {
				o.ReadOnly = true
				o.Image = "/tiler-bench-plan/app.tar"
				o.NoProbe = true
				o.Mode = "read"
				o.BenchDir = "work"
				o.BenchDirSet = true
			},
			want: "--bench-dir",
		},
		{
			name: "readonly mode both",
			edit: func(o *Options) {
				o.ReadOnly = true
				o.Image = "/tiler-bench-plan/app.tar"
				o.NoProbe = true
				o.Mode = "both"
			},
			want: "--mode read",
		},
		{
			name: "readonly mode write",
			edit: func(o *Options) {
				o.ReadOnly = true
				o.Image = "/tiler-bench-plan/app.tar"
				o.NoProbe = true
				o.Mode = "write"
			},
			want: "--mode read",
		},
		{
			name: "read image without no-probe",
			edit: func(o *Options) {
				o.Mode = "read"
				o.Image = "/tiler-bench-plan/app.tar"
			},
			want: "--no-probe",
		},
		{
			name: "tar with compress",
			edit: func(o *Options) { o.Compress = "gzip" },
			want: "--compress",
		},
		{
			name: "bad format",
			edit: func(o *Options) { o.Format = "xml" },
			want: "--format",
		},
		{
			name: "bad dmg compress",
			edit: func(o *Options) {
				o.Type = "dmg"
				o.Compress = "zstd"
			},
			fs:   writableVol("darwin"),
			want: "--compress",
		},
		{
			name: "bad squashfs compress",
			edit: func(o *Options) {
				o.Type = "squashfs"
				o.Compress = "ULFO"
			},
			fs:   writableVol("linux"),
			want: "--compress",
		},
		{
			name: "bad erofs compress",
			edit: func(o *Options) {
				o.Type = "erofs"
				o.Compress = "xz"
			},
			fs:   writableVol("linux"),
			want: "--compress",
		},
		{
			name: "size zero",
			edit: func(o *Options) { o.Size = "0" },
			want: "--size",
		},
		{
			name: "file-size zero",
			edit: func(o *Options) { o.FileSize = "0M" },
			want: "--file-size",
		},
		{
			name: "illegal size unit",
			edit: func(o *Options) { o.Size = "2Mi" },
			want: "--size",
		},
		{
			name: "decimal size",
			edit: func(o *Options) { o.FileSize = "1.5G" },
			want: "--file-size",
		},
		{
			name: "bench-dir absolute path",
			edit: func(o *Options) {
				o.BenchDir = "/outside/bench"
				o.BenchDirSet = true
			},
			want: "--bench-dir",
		},
		{
			name: "bench-dir explicit empty",
			edit: func(o *Options) {
				o.BenchDir = ""
				o.BenchDirSet = true
			},
			want: "--bench-dir",
		},
		{
			name: "bench-dir dot slash",
			edit: func(o *Options) {
				o.BenchDir = "./x"
				o.BenchDirSet = true
			},
			want: "--bench-dir",
		},
		{
			name: "bench-dir dot dot slash",
			edit: func(o *Options) {
				o.BenchDir = "../x"
				o.BenchDirSet = true
			},
			want: "--bench-dir",
		},
		{
			name: "bench-dir dot",
			edit: func(o *Options) {
				o.BenchDir = "."
				o.BenchDirSet = true
			},
			want: "--bench-dir",
		},
		{
			name: "bench-dir dot dot",
			edit: func(o *Options) {
				o.BenchDir = ".."
				o.BenchDirSet = true
			},
			want: "--bench-dir",
		},
		{
			name: "bench-dir embedded dot dot",
			edit: func(o *Options) {
				o.BenchDir = "a/../b"
				o.BenchDirSet = true
			},
			want: "--bench-dir",
		},
		{
			name: "src absolute path",
			edit: func(o *Options) {
				o.Src = "/outside/src"
				o.SrcSet = true
			},
			want: "--src",
		},
		{
			name: "src explicit empty",
			edit: func(o *Options) {
				o.Src = ""
				o.SrcSet = true
			},
			want: "--src",
		},
		{
			name: "src dot slash",
			edit: func(o *Options) {
				o.Src = "./x"
				o.SrcSet = true
			},
			want: "--src",
		},
		{
			name: "src dot dot slash",
			edit: func(o *Options) {
				o.Src = "../x"
				o.SrcSet = true
			},
			want: "--src",
		},
		{
			name: "src dot",
			edit: func(o *Options) {
				o.Src = "."
				o.SrcSet = true
			},
			want: "--src",
		},
		{
			name: "src dot dot",
			edit: func(o *Options) {
				o.Src = ".."
				o.SrcSet = true
			},
			want: "--src",
		},
		{
			name: "src embedded dot dot",
			edit: func(o *Options) {
				o.Src = "a/../b"
				o.SrcSet = true
			},
			want: "--src",
		},
		{
			name: "bench-dir not empty",
			edit: func(o *Options) {
				o.BenchDir = "work"
				o.BenchDirSet = true
			},
			fs: memFS{nodes: map[string]node{
				volPath:           {dir: true, writable: true},
				volPath + "/work": {dir: true, names: []string{"old"}},
			}}.FS("linux"),
			want: "not empty",
		},
		{
			name: "bench-dir is a file",
			edit: func(o *Options) {
				o.BenchDir = "work"
				o.BenchDirSet = true
			},
			fs: memFS{nodes: map[string]node{
				volPath:           {dir: true, writable: true},
				volPath + "/work": {dir: false},
			}}.FS("linux"),
			want: "must be a directory",
		},
		{
			name: "runtime-dir inside path",
			edit: func(o *Options) { o.RuntimeDir = volPath + "/rt" },
			fs: memFS{nodes: map[string]node{
				volPath:         {dir: true, writable: true},
				volPath + "/rt": {dir: true},
			}}.FS("linux"),
			want: "--runtime-dir",
		},
		{
			name: "path inside runtime-dir",
			edit: func(o *Options) {
				o.Path = volPath + "/data"
				o.RuntimeDir = volPath
			},
			fs: memFS{nodes: map[string]node{
				volPath:           {dir: true},
				volPath + "/data": {dir: true, writable: true},
			}}.FS("linux"),
			want: "--runtime-dir",
		},
		{
			name: "runtime-dir equals path",
			edit: func(o *Options) { o.RuntimeDir = volPath },
			want: "--runtime-dir",
		},
		{
			name: "runtime subdir not empty",
			edit: func(o *Options) { o.RuntimeDir = rtPath },
			fs: memFS{nodes: map[string]node{
				volPath:             {dir: true, writable: true},
				rtPath:              {dir: true},
				rtPath + "/runtime": {dir: true, names: []string{"old"}},
			}}.FS("linux"),
			want: "not empty",
		},
		{
			name: "runtime subdir is a file",
			edit: func(o *Options) { o.RuntimeDir = rtPath },
			fs: memFS{nodes: map[string]node{
				volPath:             {dir: true, writable: true},
				rtPath:              {dir: true},
				rtPath + "/runtime": {dir: false},
			}}.FS("linux"),
			want: "must be a directory",
		},
		{
			name: "runtime-dir is a file",
			edit: func(o *Options) { o.RuntimeDir = rtPath },
			fs: memFS{nodes: map[string]node{
				volPath: {dir: true, writable: true},
				rtPath:  {dir: false},
			}}.FS("linux"),
			want: "must be a directory",
		},
		{
			name: "unwritable path",
			fs: memFS{nodes: map[string]node{
				volPath: {dir: true, writable: false},
			}}.FS("linux"),
			want: "--readonly --image",
		},
		{
			name: "src while readonly",
			edit: func(o *Options) {
				o.ReadOnly = true
				o.Image = "/tiler-bench-plan/app.tar"
				o.NoProbe = true
				o.Mode = "read"
				o.Src = "payload"
				o.SrcSet = true
			},
			want: "--src",
		},
		{
			name: "src while reading an image",
			edit: func(o *Options) {
				o.Mode = "read"
				o.Image = "/tiler-bench-plan/app.tar"
				o.NoProbe = true
				o.Src = "payload"
				o.SrcSet = true
			},
			want: "--src",
		},
		{
			name: "bench-dir while reading an image",
			edit: func(o *Options) {
				o.Mode = "read"
				o.Image = "/tiler-bench-plan/app.tar"
				o.NoProbe = true
				o.BenchDir = "work"
				o.BenchDirSet = true
			},
			want: "--bench-dir",
		},
		{
			name: "type required off darwin",
			edit: func(o *Options) { o.Type = "" },
			fs:   writableVol("linux"),
			want: "--type",
		},
		{
			name: "dmg off darwin",
			edit: func(o *Options) { o.Type = "dmg" },
			fs:   writableVol("linux"),
			want: "darwin",
		},
		{
			name: "squashfs off linux",
			edit: func(o *Options) { o.Type = "squashfs" },
			fs:   writableVol("darwin"),
			want: "linux",
		},
		{
			name: "negative probe ops",
			edit: func(o *Options) { o.ProbeOps = -1 },
			want: "--probe-ops",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{
				Path:     volPath,
				Type:     "tar",
				Size:     "128M",
				FileSize: "4M",
			}
			if tc.edit != nil {
				tc.edit(&opts)
			}
			files := tc.fs
			if files.Stat == nil {
				files = writableVol("linux")
			}
			_, err := Plan(opts, files)
			mustUsage(t, err, tc.want)
		})
	}
}

func mustUsage(t *testing.T, err error, substr string) {
	t.Helper()
	var usage *UsageError
	if !errors.As(err, &usage) {
		t.Fatalf("error = %v, want usage error", err)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("error = %q, want substring %q", err.Error(), substr)
	}
}

func writableVol(goos string) FS {
	return memFS{nodes: map[string]node{
		volPath: {dir: true, writable: true},
	}}.FS(goos)
}

type node struct {
	dir      bool
	writable bool
	names    []string
}

type memFS struct {
	nodes map[string]node
}

func (m memFS) FS(goos string) FS {
	return FS{
		GOOS:    goos,
		Stat:    m.stat,
		Access:  m.access,
		ReadDir: m.readDir,
	}
}

func (m memFS) stat(name string) (fs.FileInfo, error) {
	n, ok := m.nodes[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return fakeInfo{dir: n.dir}, nil
}

func (m memFS) access(name string) error {
	n, ok := m.nodes[name]
	if !ok {
		return fs.ErrNotExist
	}
	if !n.writable {
		return fs.ErrPermission
	}
	return nil
}

func (m memFS) readDir(name string) ([]fs.DirEntry, error) {
	n, ok := m.nodes[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	if !n.dir {
		return nil, errors.New("not a directory")
	}
	out := make([]fs.DirEntry, len(n.names))
	for i, child := range n.names {
		out[i] = fakeEntry{name: child}
	}
	return out, nil
}

type fakeInfo struct{ dir bool }

func (fakeInfo) Name() string { return "n" }
func (fakeInfo) Size() int64  { return 0 }
func (f fakeInfo) Mode() fs.FileMode {
	if f.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool      { return f.dir }
func (fakeInfo) Sys() any           { return nil }

type fakeEntry struct{ name string }

func (e fakeEntry) Name() string               { return e.name }
func (fakeEntry) IsDir() bool                  { return false }
func (fakeEntry) Type() fs.FileMode            { return 0 }
func (e fakeEntry) Info() (fs.FileInfo, error) { return fakeInfo{}, nil }
