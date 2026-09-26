package domain

import (
	"errors"
	"fmt"
	"testing"
)

func TestWouldCreateCycle_SelfEdge(t *testing.T) {
	calls := 0
	next := func(id string) ([]string, error) {
		calls++
		return nil, nil
	}
	got, err := WouldCreateCycle("a", "a", next)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if !got {
		t.Error("WouldCreateCycle(a, a, ...) = false, want true (a self edge is always a cycle)")
	}
	if calls != 0 {
		t.Errorf("next was called %d times for a self edge, want 0 (short-circuited)", calls)
	}
}

func TestWouldCreateCycle_TwoCycle(t *testing.T) {
	// Existing edge b -> a. Adding a -> b would close a 2-cycle.
	graph := map[string][]string{"b": {"a"}, "a": {}}
	next := func(id string) ([]string, error) { return graph[id], nil }

	got, err := WouldCreateCycle("a", "b", next)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if !got {
		t.Error("WouldCreateCycle(a, b, ...) = false, want true (b already leads back to a)")
	}
}

func TestWouldCreateCycle_TwoNodesNoCycle(t *testing.T) {
	// Existing edge b -> c, c is a dead end. Adding a -> b does not cycle.
	graph := map[string][]string{"b": {"c"}, "c": {}}
	next := func(id string) ([]string, error) { return graph[id], nil }

	got, err := WouldCreateCycle("a", "b", next)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if got {
		t.Error("WouldCreateCycle(a, b, ...) = true, want false (no path from b back to a)")
	}
}

// TestWouldCreateCycle_Diamond checks a diamond-shaped DAG (two paths
// converging on the same descendant) is not mistaken for a cycle, and that
// the shared descendant is visited only once (the walk is deduplicated).
func TestWouldCreateCycle_Diamond(t *testing.T) {
	//     d0
	//    /  \
	//   d1   d2
	//    \  /
	//     d3 (dead end)
	graph := map[string][]string{
		"d0": {"d1", "d2"},
		"d1": {"d3"},
		"d2": {"d3"},
		"d3": {},
	}
	visits := map[string]int{}
	next := func(id string) ([]string, error) {
		visits[id]++
		return graph[id], nil
	}

	got, err := WouldCreateCycle("a", "d0", next)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if got {
		t.Error("WouldCreateCycle on a diamond with no cycle = true, want false")
	}
	if visits["d3"] != 1 {
		t.Errorf("d3 was visited %d times via two converging paths, want exactly 1 (deduplicated)", visits["d3"])
	}
}

// TestWouldCreateCycle_DiamondWithCycle checks a diamond whose shared
// descendant leads back to "from" is correctly reported as a cycle.
func TestWouldCreateCycle_DiamondWithCycle(t *testing.T) {
	graph := map[string][]string{
		"d0": {"d1", "d2"},
		"d1": {"d3"},
		"d2": {"d3"},
		"d3": {"a"}, // leads back to "from"
	}
	next := func(id string) ([]string, error) { return graph[id], nil }

	got, err := WouldCreateCycle("a", "d0", next)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if !got {
		t.Error("WouldCreateCycle on a diamond converging into a's descendant = false, want true")
	}
}

// TestWouldCreateCycle_LongChainNoCycle builds a 100k-node linear chain with
// no path back to "from" and checks the iterative walk completes without
// stack overflow and correctly reports no cycle.
func TestWouldCreateCycle_LongChainNoCycle(t *testing.T) {
	const n = 100_000
	next := func(id string) ([]string, error) {
		var i int
		if _, err := fmt.Sscanf(id, "b%d", &i); err != nil {
			return nil, nil // "from" or an unknown node: dead end
		}
		if i+1 >= n {
			return nil, nil
		}
		return []string{fmt.Sprintf("b%d", i+1)}, nil
	}

	got, err := WouldCreateCycle("a", "b0", next)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if got {
		t.Error("WouldCreateCycle on a long acyclic chain = true, want false")
	}
}

// TestWouldCreateCycle_LongChainWithCycle is the same 100k-node chain, but
// the tail now points back to "from": the walk must still find it.
func TestWouldCreateCycle_LongChainWithCycle(t *testing.T) {
	const n = 100_000
	next := func(id string) ([]string, error) {
		var i int
		if _, err := fmt.Sscanf(id, "b%d", &i); err != nil {
			return nil, nil
		}
		if i+1 >= n {
			return []string{"a"}, nil // tail leads back to "from"
		}
		return []string{fmt.Sprintf("b%d", i+1)}, nil
	}

	got, err := WouldCreateCycle("a", "b0", next)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if !got {
		t.Error("WouldCreateCycle on a long chain leading back to 'from' = false, want true")
	}
}

func TestWouldCreateCycle_ErrorPropagation(t *testing.T) {
	wantErr := errors.New("store unavailable")
	graph := map[string][]string{"b": {"c"}}
	next := func(id string) ([]string, error) {
		if id == "c" {
			return nil, wantErr
		}
		return graph[id], nil
	}

	got, err := WouldCreateCycle("a", "b", next)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if got {
		t.Error("WouldCreateCycle on error = true, want false (result is not meaningful on error, but should not spuriously report a cycle)")
	}
}

func TestWouldCreateCycle_ErrorOnFirstNode(t *testing.T) {
	wantErr := errors.New("boom")
	next := func(id string) ([]string, error) { return nil, wantErr }

	_, err := WouldCreateCycle("a", "b", next)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}
