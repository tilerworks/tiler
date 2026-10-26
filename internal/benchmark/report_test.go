package benchmark

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestReporterHumanTableColumnOrder(t *testing.T) {
	var buf bytes.Buffer
	r := mustReporter(t, FormatTable, &buf)
	mustRow(t, r, Step{
		Name:      "image_create",
		Seconds:   1,
		Bytes:     i64(2048),
		RateBytes: i64(chunkSize),
		Notes:     "tar.gz",
	})
	mustRow(t, r, Step{Name: "image_attach", Seconds: 0.5, Notes: "readonly"})
	mustRow(t, r, Step{Name: "read_image_1", Seconds: 0, Bytes: i64(chunkSize), Notes: "purge=yes"})
	mustRow(t, r, Step{Name: "read_image_2", Seconds: 2, Bytes: i64(chunkSize), Notes: "warm"})

	want := "" +
		"step                  seconds        bytes      MiB/s        notes\n" +
		"------------------ ---------- ------------ ---------- ------------\n" +
		"image_create            1.000         2048        1.0       tar.gz\n" +
		"image_attach            0.500            -          -     readonly\n" +
		"read_image_1            0.000      1048576        inf    purge=yes\n" +
		"read_image_2            2.000      1048576        0.5         warm\n"
	if buf.String() != want {
		t.Fatalf("table =\n%s\nwant =\n%s", buf.String(), want)
	}
}

func TestReporterTSVColumnOrder(t *testing.T) {
	var buf bytes.Buffer
	r := mustReporter(t, FormatTSV, &buf)
	mustRow(t, r, Step{
		Name:      "image_create",
		Seconds:   1,
		Bytes:     i64(2048),
		RateBytes: i64(chunkSize),
		Notes:     "tar.gz",
	})
	mustRow(t, r, Step{Name: "image_attach", Seconds: 0.5, Notes: "readonly"})
	mustRow(t, r, Step{Name: "read_image_1", Seconds: 0, Bytes: i64(chunkSize), Notes: "purge=yes"})
	mustRow(t, r, Step{Name: "read_image_2", Seconds: 2, Bytes: i64(chunkSize), Notes: "warm"})

	want := "" +
		"image_create\t1.000\t2048\t1.0\ttar.gz\n" +
		"image_attach\t0.500\t-\t-\treadonly\n" +
		"read_image_1\t0.000\t1048576\tinf\tpurge=yes\n" +
		"read_image_2\t2.000\t1048576\t0.5\twarm\n"
	if buf.String() != want {
		t.Fatalf("tsv =\n%s\nwant =\n%s", buf.String(), want)
	}
	if strings.Contains(buf.String(), "step\t") {
		t.Fatalf("tsv has a header: %q", buf.String())
	}
}

func TestReporterJSONBuffersUntilEncode(t *testing.T) {
	var buf bytes.Buffer
	r := mustReporter(t, FormatJSON, &buf)
	mustRow(t, r, Step{
		Name:      "image_create",
		Seconds:   1,
		Bytes:     i64(2048),
		RateBytes: i64(chunkSize),
		Notes:     "tar.gz",
	})
	mustRow(t, r, Step{Name: "image_attach", Seconds: 0.5, Notes: "readonly"})
	mustRow(t, r, Step{Name: "read_image_1", Seconds: 0, Bytes: i64(chunkSize), Notes: "purge=yes"})
	if buf.Len() != 0 {
		t.Fatalf("json reporter wrote early: %q", buf.String())
	}
	err := writeJSONReport(Decision{
		Type:     "tar",
		Path:     "/vol",
		Mode:     "both",
		Size:     chunkSize,
		FileSize: 4,
		Format:   FormatJSON,
	}, "/tmp/rt", deviceInfo{VolumeDevice: "/dev/disk1", DF: "/dev/disk1 1 2 3 4% /vol"}, nil, r.Steps(), &buf)
	if err != nil {
		t.Fatalf("writeJSONReport: %v", err)
	}
	var got jsonReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if got.Type != "tar" || got.Path != "/vol" || got.Device.VolumeDevice != "/dev/disk1" {
		t.Fatalf("meta = %+v", got)
	}
	if got.Probe != nil || got.ProbeEnabled {
		t.Fatalf("probe = %+v enabled=%v", got.Probe, got.ProbeEnabled)
	}
	if len(got.Steps) != 3 {
		t.Fatalf("steps = %+v", got.Steps)
	}
	if got.Steps[0].MiBS == nil || *got.Steps[0].MiBS != 1.0 {
		t.Fatalf("create mib_s = %v", got.Steps[0].MiBS)
	}
	if got.Steps[1].Bytes != nil || got.Steps[1].MiBS != nil {
		t.Fatalf("attach should be null: %+v", got.Steps[1])
	}
	if got.Steps[2].MiBS != nil {
		t.Fatalf("zero-second read mib_s = %v, want null", got.Steps[2].MiBS)
	}
}

func mustReporter(t *testing.T, format string, buf *bytes.Buffer) *Reporter {
	t.Helper()
	r, err := NewReporter(format, buf)
	if err != nil {
		t.Fatalf("NewReporter: %v", err)
	}
	return r
}

func mustRow(t *testing.T, r *Reporter, step Step) {
	t.Helper()
	if err := r.Row(step); err != nil {
		t.Fatalf("Row: %v", err)
	}
}

func i64(n int64) *int64 { return &n }
