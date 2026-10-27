package features

import (
	"fmt"
	"testing"
)

func paths(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s/%d.go", prefix, i)
	}
	return out
}

func TestLargeUpdateNeedsBothThresholds(t *testing.T) {
	four := paths("a", 4)
	for _, tc := range []struct {
		name      string
		assigned  []string
		current   []string
		differing int
		total     int
		large     bool
	}{
		{"unchanged", four, four, 0, 4, false},
		{"one added", four, append(four[:4:4], "b.go"), 1, 5, false},
		// At the path count, below the change: 3 of 7.
		{"three added", four, append(four[:4:4], paths("b", 3)...), 3, 7, false},
		// At both: 4 of 8 is exactly 50%.
		{"four added", four, append(four[:4:4], paths("b", 4)...), 4, 8, true},
		// The issue's example: 4 paths plus 6 new is a 60% change.
		{"six added", four, append(four[:4:4], paths("b", 6)...), 6, 10, true},
		// Above the change, below the path count: 2 of 2.
		{"one replaced", []string{"a.go"}, []string{"b.go"}, 2, 2, false},
		// Above both: 3 of 3.
		{"all replaced", []string{"a.go"}, []string{"b.go", "c.go"}, 3, 3, true},
		{"paths removed", four, four[:1], 3, 4, true},
		{"nothing either side", nil, nil, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			differing, total := PathChange(tc.assigned, tc.current)
			if differing != tc.differing || total != tc.total {
				t.Fatalf("PathChange = %d of %d, want %d of %d", differing, total, tc.differing, tc.total)
			}
			if got := LargeUpdate(tc.assigned, tc.current, 0.5, 3); got != tc.large {
				t.Fatalf("LargeUpdate = %v, want %v", got, tc.large)
			}
		})
	}
	// Both thresholds are configurable; the statement that claims pending
	// items applies the same rule (prreview's assigner tests).
	if !LargeUpdate(four, append(four[:4:4], "b.go"), 0.2, 1) || LargeUpdate(four, append(four[:4:4], paths("b", 6)...), 0.7, 3) {
		t.Fatal("LargeUpdate ignores its thresholds")
	}
}
