//go:build linux

package doctor

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestProbeFileLimitFoundForms(t *testing.T) {
	refuseFrom := func(size int64, errno syscall.Errno) func(string, int64) error {
		return func(_ string, probe int64) error {
			if probe >= size {
				return fmt.Errorf("truncate: %w", errno)
			}
			return nil
		}
	}
	fourGiB := int64(4) << 30
	eightGiB := int64(8) << 30

	tests := []struct {
		name       string
		probe      func(string, int64) error
		opts       Options
		wantStatus string
		wantDetail string
		wantLimit  *int64
	}{
		{
			name:       "efbig limit with split off warns with guidance",
			probe:      refuseFrom(eightGiB, syscall.EFBIG),
			opts:       Options{},
			wantStatus: statusWarn,
			wantDetail: "single-file size limit between 4 GiB and 8 GiB; " + splitGuidanceText,
			wantLimit:  &fourGiB,
		},
		{
			name:       "einval counts as a limit refusal",
			probe:      refuseFrom(eightGiB, syscall.EINVAL),
			opts:       Options{},
			wantStatus: statusWarn,
			wantDetail: "single-file size limit between 4 GiB and 8 GiB",
			wantLimit:  &fourGiB,
		},
		{
			name:       "enospc counts as a limit refusal",
			probe:      refuseFrom(eightGiB, syscall.ENOSPC),
			opts:       Options{},
			wantStatus: statusWarn,
			wantDetail: "single-file size limit between 4 GiB and 8 GiB",
			wantLimit:  &fourGiB,
		},
		{
			name:       "limit with split enabled is info",
			probe:      refuseFrom(eightGiB, syscall.EFBIG),
			opts:       Options{SplitEnabled: true, SplitMode: "blocks"},
			wantStatus: statusInfo,
			wantDetail: "single-file size limit between 4 GiB and 8 GiB; split storage (blocks) is enabled",
			wantLimit:  &fourGiB,
		},
		{
			name:       "first rung refused has no measured limit",
			probe:      refuseFrom(fourGiB, syscall.EFBIG),
			opts:       Options{},
			wantStatus: statusWarn,
			wantDetail: "single-file size limit below 4 GiB; " + splitGuidanceText,
			wantLimit:  nil,
		},
		{
			name:       "refusal above the cap is never reached",
			probe:      refuseFrom(int64(1024)<<30, syscall.EFBIG),
			opts:       Options{},
			wantStatus: statusPass,
			wantDetail: "no single-file size limit below 512 GiB",
			wantLimit:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := probeFileLimit(nameFileLimitImages, "/store/images", tt.opts, tt.probe)
			if got.Status != tt.wantStatus {
				t.Fatalf("status = %s, want %s (%s)", got.Status, tt.wantStatus, got.Detail)
			}
			if tt.wantDetail != "" && !strings.Contains(got.Detail, tt.wantDetail) {
				t.Fatalf("detail = %q, want it to contain %q", got.Detail, tt.wantDetail)
			}
			switch {
			case tt.wantLimit == nil && got.LimitBytes != nil:
				t.Fatalf("LimitBytes = %d, want unset", *got.LimitBytes)
			case tt.wantLimit != nil && got.LimitBytes == nil:
				t.Fatalf("LimitBytes unset, want %d", *tt.wantLimit)
			case tt.wantLimit != nil && *got.LimitBytes != *tt.wantLimit:
				t.Fatalf("LimitBytes = %d, want %d", *got.LimitBytes, *tt.wantLimit)
			}
		})
	}
}

func TestIsSizeLimitErrClassifiesErrnos(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EFBIG, syscall.EINVAL, syscall.ENOSPC} {
		wrapped := fmt.Errorf("truncate %s: %w", "probe", errno)
		if !isSizeLimitErr(wrapped) {
			t.Fatalf("isSizeLimitErr(%v) = false, want true", wrapped)
		}
	}
	for _, err := range []error{os.ErrPermission, syscall.EIO, syscall.ENOENT} {
		if isSizeLimitErr(err) {
			t.Fatalf("isSizeLimitErr(%v) = true, want false", err)
		}
	}
}
