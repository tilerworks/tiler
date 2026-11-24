package checkpoint

import (
	"errors"
	"reflect"
	"testing"
)

func TestMountSnapshotRetriesGrowth(t *testing.T) {
	calls := 0
	got, err := mountSnapshot(func(buf []int) (int, error) {
		calls++
		if buf == nil {
			return 1, nil
		}
		if calls == 2 {
			return len(buf), nil
		}
		copy(buf, []int{1, 2})
		return 2, nil
	})
	if err != nil || !reflect.DeepEqual(got, []int{1, 2}) || calls != 3 {
		t.Fatalf("snapshot = %v, calls = %d, err = %v", got, calls, err)
	}
}

func TestMountSnapshotEmptyAndErrors(t *testing.T) {
	want := errors.New("read failure")
	if _, err := mountSnapshot(func([]int) (int, error) { return 0, want }); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	got, err := mountSnapshot(func([]int) (int, error) { return 0, nil })
	if err != nil || len(got) != 0 {
		t.Fatalf("empty snapshot = %v, err = %v", got, err)
	}
	if _, err := mountSnapshot(func(buf []int) (int, error) { return len(buf), nil }); err == nil {
		t.Fatal("accepted a constantly growing table")
	}
}
