package mountinfo

import (
	"strings"
	"testing"
)

const (
	testImages  = "/home/u/.tiler/images"
	testRuntime = "/home/u/.tiler/runtime"
)

func testPrefixes() Prefixes {
	return Prefixes{RuntimeDir: testRuntime, ImagesDir: testImages}
}

func testTable() []Mount {
	return []Mount{
		{Point: "/", FSType: "ext4", Source: "/dev/sda1", SuperOpts: "rw,relatime"},
		{
			Point: testRuntime + "/ro0", FSType: "fuse",
			Source: testImages + "/alice/app/image.sqsh",
		},
		{
			Point: testRuntime + "/ro1", FSType: "fuse",
			Source: testImages + "/alice/app/incr/fedcba9876543210.sqsh",
		},
		{
			Point: "/ws/view", FSType: "fuse.fuse-overlayfs", Source: "fuse-overlayfs",
			Upper:  testRuntime + "/up-2049-7/upper",
			Lowers: []string{testRuntime + "/ro1", testRuntime + "/ro0"},
		},
		{
			Point: "/ws/view/nested", FSType: "overlay", Source: "overlay",
			Upper:  testRuntime + "/up-9-9/upper",
			Lowers: []string{testRuntime + "/ro0"},
		},
		{Point: "/ws/ro", FSType: "fuse", Source: testImages + "/bob/tool/image.sqsh"},
		{
			Point: "/ws/mixed", FSType: "fuse.fuse-overlayfs", Source: "fuse-overlayfs",
			Upper:  testRuntime + "/up-3-4/upper",
			Lowers: []string{testRuntime + "/ro-a", testRuntime + "/ro-b"},
		},
		{Point: testRuntime + "/ro-a", FSType: "fuse", Source: testImages + "/alice/app/image.sqsh"},
		{Point: testRuntime + "/ro-b", FSType: "fuse", Source: testImages + "/bob/tool/image.sqsh"},
		{
			Point: "/ws/opaque", FSType: "fuse.fuse-overlayfs", Source: "fuse-overlayfs",
			Upper:  testRuntime + "/up-5-6/upper",
			Lowers: []string{testRuntime + "/gone"},
		},
	}
}

const parseFixture = `22 30 0:21 / /sys rw,nosuid,nodev,noexec,relatime shared:7 - sysfs sysfs rw
39 30 0:53 / /home/u/.tiler/runtime/ro0 rw,nosuid,nodev,relatime - fuse /home/u/.tiler/images/alice/app/image.sqsh rw,user_id=1000,group_id=1000
40 30 0:55 / /ws/view rw,relatime - overlay overlay rw,lowerdir=/home/u/.tiler/runtime/ro1:/home/u/.tiler/runtime/ro0,upperdir=/home/u/.tiler/runtime/up-2049-7/upper,workdir=/home/u/.tiler/runtime/up-2049-7/work
41 30 0:56 / /mnt/spaced\040dir rw,relatime - fuse.squashfuse_ll squashfuse_ll rw,user_id=1000,group_id=1000
`

func TestParseMountinfoFixture(t *testing.T) {
	mounts, err := Parse(strings.NewReader(parseFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 4 {
		t.Fatalf("parsed %d mounts", len(mounts))
	}
	sys := mounts[0]
	if sys.Point != "/sys" || sys.FSType != "sysfs" || sys.Source != "sysfs" ||
		sys.SuperOpts != "rw" || sys.Upper != "" || sys.Lowers != nil {
		t.Fatalf("sysfs mount = %+v", sys)
	}
	ro := mounts[1]
	if ro.Point != testRuntime+"/ro0" || ro.FSType != "fuse" ||
		ro.Source != testImages+"/alice/app/image.sqsh" ||
		ro.SuperOpts != "rw,user_id=1000,group_id=1000" {
		t.Fatalf("fuse mount = %+v", ro)
	}
	overlay := mounts[2]
	if overlay.Point != "/ws/view" || overlay.FSType != "overlay" {
		t.Fatalf("overlay mount = %+v", overlay)
	}
	if overlay.Upper != testRuntime+"/up-2049-7/upper" {
		t.Fatalf("overlay upper = %q", overlay.Upper)
	}
	wantLowers := []string{testRuntime + "/ro1", testRuntime + "/ro0"}
	if len(overlay.Lowers) != 2 || overlay.Lowers[0] != wantLowers[0] ||
		overlay.Lowers[1] != wantLowers[1] {
		t.Fatalf("overlay lowers = %q", overlay.Lowers)
	}
	if mounts[3].Point != "/mnt/spaced dir" {
		t.Fatalf("escaped point = %q", mounts[3].Point)
	}
}

func TestParseReportsBadLineNumber(t *testing.T) {
	text := "22 30 0:21 / /sys rw,relatime shared:7 - sysfs sysfs rw\n" +
		"\n" +
		"this line has no separator at all\n"
	_, err := Parse(strings.NewReader(text))
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseReportsTruncatedEntry(t *testing.T) {
	_, err := Parse(strings.NewReader("1 2 0:0 / /x opts - fstype\n"))
	if err == nil || !strings.Contains(err.Error(), "line 1") ||
		!strings.Contains(err.Error(), "truncated") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecidePlainDirectoryIsFull(t *testing.T) {
	got, err := Decide("/home/u/work", testTable(), testPrefixes(), Explicit{Image: "alice/app"})
	if err != nil {
		t.Fatal(err)
	}
	want := Decision{
		Mode: ModeFull, Name: Name{Owner: "alice", Repo: "app"}, SrcDir: "/home/u/work",
	}
	if got != want {
		t.Fatalf("decision = %+v", got)
	}
}

func TestDecidePlainDirectoryNeedsImage(t *testing.T) {
	_, err := Decide("/home/u/work", testTable(), testPrefixes(), Explicit{})
	if err == nil || !strings.Contains(err.Error(), "--image") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecideMergedViewIsIncremental(t *testing.T) {
	got, err := Decide("/ws/view/deep/sub", testTable(), testPrefixes(), Explicit{})
	if err != nil {
		t.Fatal(err)
	}
	want := Decision{
		Mode:       ModeIncr,
		Name:       Name{Owner: "alice", Repo: "app"},
		MountPoint: "/ws/view",
		Upper:      testRuntime + "/up-2049-7/upper",
	}
	if got != want {
		t.Fatalf("decision = %+v", got)
	}
}

func TestDecideLongestMountPointWins(t *testing.T) {
	got, err := Decide("/ws/view/nested/deep", testTable(), testPrefixes(), Explicit{})
	if err != nil {
		t.Fatal(err)
	}
	if got.MountPoint != "/ws/view/nested" || got.Upper != testRuntime+"/up-9-9/upper" {
		t.Fatalf("decision = %+v", got)
	}
	if got.Mode != ModeIncr || got.Name != (Name{Owner: "alice", Repo: "app"}) {
		t.Fatalf("decision = %+v", got)
	}
}

func TestDecideRuntimePrefixRefused(t *testing.T) {
	for _, cwd := range []string{testRuntime, testRuntime + "/up-2049-7/upper/x"} {
		_, err := Decide(cwd, testTable(), testPrefixes(), Explicit{Image: "alice/app"})
		if err == nil || !strings.Contains(err.Error(), "runtime directory") {
			t.Fatalf("cwd %s: err = %v", cwd, err)
		}
	}
}

func TestDecideStaleMarkerRefused(t *testing.T) {
	_, err := Decide("/ws/gone", testTable(), testPrefixes(), Explicit{Stale: true})
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecideReadOnlyViewRefusedImplicitly(t *testing.T) {
	_, err := Decide("/ws/ro/deep", testTable(), testPrefixes(), Explicit{})
	if err == nil || !strings.Contains(err.Error(), "read-only view") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecideReadOnlyViewRefusesIncr(t *testing.T) {
	_, err := Decide("/ws/ro", testTable(), testPrefixes(), Explicit{Incr: true})
	if err == nil || !strings.Contains(err.Error(), "without an upper") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecideReadOnlyViewFullPacksMountPoint(t *testing.T) {
	got, err := Decide("/ws/ro/deep/inside", testTable(), testPrefixes(),
		Explicit{Full: true, Image: "bob/tool"})
	if err != nil {
		t.Fatal(err)
	}
	want := Decision{
		Mode: ModeFull, Name: Name{Owner: "bob", Repo: "tool"},
		SrcDir: "/ws/ro", MountPoint: "/ws/ro",
	}
	if got != want {
		t.Fatalf("decision = %+v", got)
	}
}

func TestDecideImageConflictRefused(t *testing.T) {
	_, err := Decide("/ws/view/x", testTable(), testPrefixes(), Explicit{Image: "carol/other"})
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecideReadOnlyViewConflictRefused(t *testing.T) {
	_, err := Decide("/ws/ro/x", testTable(), testPrefixes(),
		Explicit{Full: true, Image: "alice/app"})
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecideConsistentImageAccepted(t *testing.T) {
	got, err := Decide("/ws/view/x", testTable(), testPrefixes(), Explicit{Image: "alice/app"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModeIncr || got.Name != (Name{Owner: "alice", Repo: "app"}) {
		t.Fatalf("decision = %+v", got)
	}
}

func TestDecideMixedLowersRefused(t *testing.T) {
	_, err := Decide("/ws/mixed/x", testTable(), testPrefixes(), Explicit{})
	if err == nil || !strings.Contains(err.Error(), "mixes") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecideBothFlagsUsageError(t *testing.T) {
	_, err := Decide("/home/u/work", testTable(), testPrefixes(), Explicit{Full: true, Incr: true})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecideIncrOutsideViewRefused(t *testing.T) {
	_, err := Decide("/home/u/work", testTable(), testPrefixes(),
		Explicit{Incr: true, Image: "alice/app"})
	if err == nil || !strings.Contains(err.Error(), "plain directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecideIncrementalRejectsDirArgument(t *testing.T) {
	_, err := Decide("/ws/view/x", testTable(), testPrefixes(), Explicit{HasDir: true})
	if err == nil || !strings.Contains(err.Error(), "no source directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecideOpaqueViewNeedsExplicitImage(t *testing.T) {
	_, err := Decide("/ws/opaque/x", testTable(), testPrefixes(), Explicit{})
	if err == nil || !strings.Contains(err.Error(), "--image") {
		t.Fatalf("err = %v", err)
	}
	got, err := Decide("/ws/opaque/x", testTable(), testPrefixes(), Explicit{Image: "dave/x"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != ModeIncr || got.Name != (Name{Owner: "dave", Repo: "x"}) ||
		got.Upper != testRuntime+"/up-5-6/upper" {
		t.Fatalf("decision = %+v", got)
	}
}

func TestDecideInvalidExplicitImage(t *testing.T) {
	_, err := Decide("/home/u/work", testTable(), testPrefixes(), Explicit{Image: "no-slash"})
	if err == nil {
		t.Fatal("expected name error")
	}
}

type stubIdentity struct {
	name Name
	ok   bool
	err  error
	seen []string
}

func (s *stubIdentity) Lookup(mountPoint string) (Name, bool, error) {
	s.seen = append(s.seen, mountPoint)
	return s.name, s.ok, s.err
}

func TestDecideUsesInjectedIdentitySource(t *testing.T) {
	stub := &stubIdentity{name: Name{Owner: "erin", Repo: "sess"}, ok: true}
	got, err := Decide("/ws/opaque/x", testTable(), testPrefixes(),
		Explicit{Identity: stub})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != (Name{Owner: "erin", Repo: "sess"}) || got.Mode != ModeIncr {
		t.Fatalf("decision = %+v", got)
	}
	if len(stub.seen) != 1 || stub.seen[0] != "/ws/opaque" {
		t.Fatalf("lookup calls = %v", stub.seen)
	}
	_, err = Decide("/ws/opaque/x", testTable(), testPrefixes(),
		Explicit{Identity: stub, Image: "alice/app"})
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("err = %v", err)
	}
}

func TestLibraryNameMapping(t *testing.T) {
	good := []struct {
		path string
		want Name
	}{
		{testImages + "/alice/app/image.sqsh", Name{"alice", "app"}},
		{testImages + "/alice/app/incr/fedcba9876543210.sqsh", Name{"alice", "app"}},
	}
	for _, tc := range good {
		got, ok := LibraryName(testImages, tc.path)
		if !ok || got != tc.want {
			t.Errorf("LibraryName(%q) = %v %v", tc.path, got, ok)
		}
	}
	bad := []string{
		testImages + "/alice/app/meta.yaml",
		testImages + "/alice/app/archives/0123456789abcdef/image.sqsh",
		testImages + "/alice/app/hold/0123456789abcdef/image.sqsh",
		testImages + "/alice/app/incr/not-hex.sqsh",
		testImages + "/alice/app/incr/fedcba9876543210",
		testImages + "/alice/app/sub/image.sqsh",
		testImages + "/onlyowner/image.sqsh",
		"/elsewhere/alice/app/image.sqsh",
		testImages + "/bad owner/app/image.sqsh",
	}
	for _, path := range bad {
		if got, ok := LibraryName(testImages, path); ok {
			t.Errorf("LibraryName(%q) = %v, want no identity", path, got)
		}
	}
	if _, ok := LibraryName("", testImages+"/alice/app/image.sqsh"); ok {
		t.Error("empty images dir resolved an identity")
	}
}

func TestPathIdentityUnknownMountPoint(t *testing.T) {
	identity := PathIdentity{Table: testTable(), ImagesDir: testImages}
	name, ok, err := identity.Lookup("/ws/never-mounted")
	if err != nil || ok {
		t.Fatalf("lookup = %v %v %v", name, ok, err)
	}
}

func TestDecideNeverTouchesThePath(t *testing.T) {
	// The merged view point does not exist on this machine; the
	// decision still names it from the table alone.
	got, err := Decide("/ws/view/absent/deep", testTable(), testPrefixes(), Explicit{})
	if err != nil {
		t.Fatal(err)
	}
	if got.MountPoint != "/ws/view" || got.Mode != ModeIncr {
		t.Fatalf("decision = %+v", got)
	}
}
