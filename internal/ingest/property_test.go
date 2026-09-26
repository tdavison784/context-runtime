package ingest

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
)

// Generated-history properties over ingestion (gate: INV-04/06/08/09 and
// atomicity). Histories mix authorities, agents, directives, lifecycle
// commands, replacements, restatements and rejected events, through W3's
// real lifecycle service.

type genStep struct {
	p domain.Principal
	e domain.Event
}

// genHistory is a deterministic history for seed.
func genHistory(seed uint64, n int) []genStep {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	keys := []string{"k1", "k2", "k3"}
	pick := func(xs []string) string { return xs[rng.IntN(len(xs))] }
	var out []genStep
	for i := range n {
		id := fmt.Sprintf("g%d-%d", seed, i)
		key, text := pick(keys), pick([]string{"alpha", "beta", "gamma"})
		var body string
		switch rng.IntN(7) {
		case 0:
			body = "## Goal [" + key + "]\nDo " + text + ".\n"
		case 1:
			body = "## Pinned\n- [" + key + "] Rule " + text + ".\n"
		case 2:
			body = "## Working\n- step " + text + "\n"
		case 3:
			body = "## Resolve [" + key + "]\n"
		case 4:
			body = "## Unpin [" + key + "]\n"
		case 5:
			body = "## Remember\n- fact " + text + "\n"
		default:
			body = "plain " + text
		}
		agent := pick([]string{"A", "B"})
		switch a := rng.IntN(5); a {
		case 0:
			p := phase2Principal(domain.AuthoritySystem, agent)
			out = append(out, genStep{p, domain.Event{EventID: id, Kind: domain.EventSystem, Spans: []domain.Span{textSpan(domain.AuthoritySystem, false, body)}}})
		case 1:
			p := phase2Principal(domain.AuthorityHarness, agent)
			out = append(out, genStep{p, domain.Event{EventID: id, Kind: domain.EventHarness, TurnBoundary: rng.IntN(2) == 0, Spans: []domain.Span{textSpan(domain.AuthorityHarness, false, body)}}})
		case 2, 3:
			p := phase2Principal(domain.AuthorityUser, agent)
			out = append(out, genStep{p, userEvent(id, body, rng.IntN(3) != 0)})
		default:
			// A SYSTEM caller carrying agent or tool text: a confused-deputy
			// attempt that must never gain the caller's authority.
			p := phase2Principal(domain.AuthoritySystem, agent)
			a := pick([]string{string(domain.AuthorityAgent), string(domain.AuthorityTool)})
			out = append(out, genStep{p, domain.Event{EventID: id, Kind: domain.EventSystem, Spans: []domain.Span{textSpan(domain.Authority(a), false, body)}}})
		}
	}
	return out
}

func newPropertyFixture(t *testing.T) *fixture {
	ms := memory.New()
	t.Cleanup(func() { ms.Close() })
	// Without the Phase 3 facet every event is rejected and the invariants
	// would hold vacuously.
	if !hasSemantic(ms) {
		t.Skip("GATE-PENDING: needs " + depW2)
	}
	f := newFixture(t, ms)
	pol := testPolicy()
	f.in.Semantic = &pol
	return f
}

// runHistory applies h, checking the invariants after every step, and
// returns each step's receipt (or nil for a rejected step).
func runHistory(t *testing.T, f *fixture, h []genStep) []*domain.IngestReceipt {
	t.Helper()
	resolved := map[string]bool{}
	out := make([]*domain.IngestReceipt, len(h))
	for i, st := range h {
		before := snapshotPhase2(t, f.s)
		r, err := f.ingest(st.p, st.e)
		if err != nil {
			if after := snapshotPhase2(t, f.s); !reflect.DeepEqual(normGolden(after), normGolden(before)) {
				t.Fatalf("step %d (%s): rejected event (%v) changed state", i, st.e.EventID, err)
			}
			continue
		}
		out[i] = &r
		checkInvariants(t, f, i, resolved)
	}
	return out
}

func checkInvariants(t *testing.T, f *fixture, step int, resolved map[string]bool) {
	t.Helper()
	f.view(func(tx store.ReadTx) error {
		items, err := tx.Items(store.ItemFilter{})
		if err != nil {
			return err
		}
		byID := map[string]domain.ContextItem{}
		for _, it := range items {
			byID[it.ID] = it
		}
		currentPerKey := map[string]string{}
		for _, it := range items {
			// INV-08-adjacent (FR-DIR-005): a resolved goal never reopens.
			if it.GoalStatus != nil {
				if *it.GoalStatus == domain.GoalResolved {
					resolved[it.ID] = true
				} else if resolved[it.ID] {
					t.Fatalf("step %d: goal %s reopened in place", step, it.ID)
				}
			}
			// INV-04: a derived item never exceeds its transcript's
			// authority, and low-authority text never becomes a
			// requirement or directive.
			if it.Role != domain.RoleTranscript && len(it.SourceRanges) > 0 {
				tr := byID[it.SourceRanges[0].TranscriptID]
				if !tr.Authority.AtLeast(it.Authority) {
					t.Fatalf("step %d: %s authority %s above its source %s", step, it.ID, it.Authority, tr.Authority)
				}
			}
			if !it.Authority.CanHoldLifecycleAuthority() && (it.IsPinned() || it.Kind == domain.KindGoal || it.DirectiveID != "") {
				t.Fatalf("step %d: %s-authority item became a requirement: %+v", step, it.Authority, it)
			}
			// One current version per key.
			if key, ok := it.CurrentKey(); ok {
				cur, err := tx.CurrentVersion(key)
				if err == nil && cur == it.ID {
					h, _ := key.CanonicalHash()
					if prev, dup := currentPerKey[h]; dup && prev != it.ID {
						t.Fatalf("step %d: two current versions of one key: %s, %s", step, prev, it.ID)
					}
					currentPerKey[h] = it.ID
				}
			}
		}
		rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes})
		if err != nil {
			return err
		}
		next := map[string][]string{}
		for _, r := range rels {
			next[r.FromID] = append(next[r.FromID], r.ToID)
			// INV-04: supersession never lets lower authority override.
			if from, to := byID[r.FromID], byID[r.ToID]; !from.Authority.AtLeast(to.Authority) {
				t.Fatalf("step %d: %s (%s) supersedes higher-authority %s (%s)", step, from.ID, from.Authority, to.ID, to.Authority)
			}
		}
		// INV-06: the supersession graph is acyclic.
		state := map[string]int{}
		var visit func(string) bool
		visit = func(id string) bool {
			switch state[id] {
			case 1:
				return false
			case 2:
				return true
			}
			state[id] = 1
			for _, n := range next[id] {
				if !visit(n) {
					return false
				}
			}
			state[id] = 2
			return true
		}
		for id := range next {
			if !visit(id) {
				t.Fatalf("step %d: supersession cycle through %s", step, id)
			}
		}
		return nil
	})
}

// TestProperty_GeneratedHistories checks the invariants after every step
// of many generated histories, then INV-09: replaying a history into a
// fresh store yields identical receipts and state, and retrying every
// committed event afterwards replays its receipt without side effects.
func TestProperty_GeneratedHistories(t *testing.T) {
	seeds := 24
	if testing.Short() {
		seeds = 4
	}
	for seed := range uint64(seeds) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			h := genHistory(seed, 40)
			f := newPropertyFixture(t)
			got := runHistory(t, f, h)

			g := newPropertyFixture(t)
			replay := runHistory(t, g, h)
			for i := range got {
				if (got[i] == nil) != (replay[i] == nil) || got[i] != nil && !reflect.DeepEqual(normReceipt(*got[i]), normReceipt(*replay[i])) {
					t.Fatalf("step %d: replay diverged", i)
				}
			}
			if !reflect.DeepEqual(normGolden(snapshotPhase2(t, f.s)), normGolden(snapshotPhase2(t, g.s))) {
				t.Fatalf("replayed state diverged")
			}

			last := f.lastSeq()
			for i, st := range h {
				if got[i] == nil {
					continue
				}
				r, err := f.ingest(st.p, st.e)
				if err != nil || !reflect.DeepEqual(normReceipt(r), normReceipt(*got[i])) {
					t.Fatalf("step %d retry: %v", i, err)
				}
			}
			if f.lastSeq() != last {
				t.Fatalf("retries allocated sequences")
			}
		})
	}
}
