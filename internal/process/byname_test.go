package process

import (
	"errors"
	"slices"
	"testing"
)

func TestPIDsByComm(t *testing.T) {
	pids, err := pidsByComm(
		func() ([]int, error) { return []int{42, 7, 99, 3}, nil },
		func(pid int) (string, bool) {
			switch pid {
			case 42, 3:
				return "sermod", true
			case 7:
				return "other", true
			default:
				return "", false
			}
		},
		"sermod",
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{3, 42}; !slices.Equal(pids, want) {
		t.Fatalf("PIDsByComm = %v, want %v", pids, want)
	}
}

func TestPIDsByCommReturnsPIDListError(t *testing.T) {
	wantErr := errors.New("cannot list PIDs")
	_, err := pidsByComm(
		func() ([]int, error) { return nil, wantErr },
		func(int) (string, bool) { return "", false },
		"sermod",
	)
	if err != wantErr {
		t.Fatalf("PIDsByComm error = %v, want %v", err, wantErr)
	}
}
