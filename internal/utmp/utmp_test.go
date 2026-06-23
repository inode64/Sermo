package utmp

import "testing"

func TestDistinctUsers(t *testing.T) {
	cases := []struct {
		name string
		in   []Session
		want int
	}{
		{"empty", nil, 0},
		{"two sessions one user", []Session{{User: "fran", Line: "pts/0"}, {User: "fran", Line: "pts/1"}}, 1},
		{"two users", []Session{{User: "fran", Line: "pts/0"}, {User: "root", Line: "tty1"}}, 2},
		{"blank user ignored", []Session{{User: "", Line: "pts/0"}, {User: "root", Line: "tty1"}}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DistinctUsers(tc.in); got != tc.want {
				t.Errorf("DistinctUsers(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
