package attest

import "testing"

func TestIsDescendant(t *testing.T) {
	// Process tree: 1 <- 100 (session root) <- 200 (bash) <- 300 (curl); 999 is
	// an unrelated process parented straight off init.
	parents := map[int]int{300: 200, 200: 100, 100: 1, 999: 1}
	parent := func(pid int) (int, bool) { p, ok := parents[pid]; return p, ok }

	cases := []struct {
		pid, ancestor int
		want          bool
	}{
		{300, 100, true},  // grandchild of the session root
		{200, 100, true},  // direct child
		{100, 100, true},  // the root itself
		{999, 100, false}, // unrelated process — the impersonation case
		{300, 999, false}, // not under 999
		{0, 100, false},   // invalid pid
		{300, 0, false},   // invalid ancestor
	}
	for _, c := range cases {
		if got := IsDescendant(c.pid, c.ancestor, parent); got != c.want {
			t.Errorf("IsDescendant(%d, %d) = %v, want %v", c.pid, c.ancestor, got, c.want)
		}
	}
}

func TestIsDescendantCycleBounded(t *testing.T) {
	// A corrupt chain that points back at itself must not loop forever.
	parent := func(pid int) (int, bool) { return pid, true }
	if IsDescendant(50, 100, parent) {
		t.Errorf("self-cycle should not resolve as descendant")
	}
}
