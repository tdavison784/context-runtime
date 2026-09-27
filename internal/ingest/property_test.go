package ingest

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
)

// Generated-history properties over ingestion (gate: INV-04/06/08/09 and
// atomicity). Histories mix authorities, agents, directives, lifecycle
// commands, replacements, restatements and rejected events, through W3's
// real lifecycle service.

type genStep struct {
	p domain.Principal
	e domain.Event
	// transition, when set, builds the step from state at its turn: an
	// obligation transition by trans.authority on the pick-th current
	// obligation (so replaying the same history makes the same choice).
	trans *genTransition
}

type genTransition struct {
	id        string
	authority domain.Authority
	to        domain.ObligationStatus
	pick      int
	stale     bool
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
		if rng.IntN(9) == 8 {
			out = append(out, genStep{trans: &genTransition{id: id,
				authority: domain.Authority(pick([]string{string(domain.AuthoritySystem), string(domain.AuthorityHarness), string(domain.AuthorityUser)})),
				to:        domain.ObligationStatus(pick([]string{string(domain.ObligationSatisfied), string(domain.ObligationBlocked), string(domain.ObligationUnresolved), string(domain.ObligationWaived)})),
				pick:      rng.IntN(8), stale: rng.IntN(6) == 0}})
			continue
		}
		switch rng.IntN(8) {
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
		case 6:
			// Claim pins declare obligations through W4 (P3-12).
			body = "## Pinned\n- [" + key + "] {obligation=tests_pass} Rule " + text + ".\n"
			if text == "gamma" {
				body = "## Pinned\n- [" + key + "] All tests must pass.\n"
			}
		default:
			body = "plain " + text
		}
		agent := pick([]string{"A", "B"})
		switch a := rng.IntN(5); a {
		case 0:
			p := phase2Principal(domain.AuthoritySystem, agent)
			out = append(out, genStep{p: p, e: domain.Event{EventID: id, Kind: domain.EventSystem, Spans: []domain.Span{textSpan(domain.AuthoritySystem, false, body)}}})
		case 1:
			p := phase2Principal(domain.AuthorityHarness, agent)
			out = append(out, genStep{p: p, e: domain.Event{EventID: id, Kind: domain.EventHarness, TurnBoundary: rng.IntN(2) == 0, Spans: []domain.Span{textSpan(domain.AuthorityHarness, false, body)}}})
		case 2, 3:
			p := phase2Principal(domain.AuthorityUser, agent)
			out = append(out, genStep{p: p, e: userEvent(id, body, rng.IntN(3) != 0)})
		default:
			// A SYSTEM caller carrying agent or tool text: a confused-deputy
			// attempt that must never gain the caller's authority.
			p := phase2Principal(domain.AuthoritySystem, agent)
			a := pick([]string{string(domain.AuthorityAgent), string(domain.AuthorityTool)})
			out = append(out, genStep{p: p, e: domain.Event{EventID: id, Kind: domain.EventSystem, Spans: []domain.Span{textSpan(domain.Authority(a), false, body)}}})
		}
	}
	return out
}

// propertyStore opens a fresh store of one backend for a property run.
type propertyStore func(t *testing.T) store.Store

func memoryPropertyStore(t *testing.T) store.Store {
	ms := memory.New()
	t.Cleanup(func() { ms.Close() })
	return ms
}

func sqlitePropertyStore(t *testing.T) store.Store { return sqlitetest.Open(t) }

func newPropertyFixture(t *testing.T, open propertyStore) *fixture {
	s := open(t)
	// Without the Phase 3 facet every event is rejected and the invariants
	// would hold vacuously.
	if !hasSemantic(s) {
		t.Skip("GATE-PENDING: needs " + depW2)
	}
	f := newFixture(t, s)
	pol := testPolicy()
	f.usePolicy(pol)
	return f
}

// runHistory applies h, checking the invariants after every step, and
// returns each step's receipt (or nil for a rejected step).
func runHistory(t *testing.T, f *fixture, h []genStep) []*domain.IngestReceipt {
	t.Helper()
	resolved := map[string]bool{}
	frozen := map[string]domain.ObligationStatus{}
	out := make([]*domain.IngestReceipt, len(h))
	for i, st := range h {
		if st.trans != nil {
			var ok bool
			if st.p, st.e, ok = f.transitionStep(*st.trans); !ok {
				continue
			}
			h[i] = st
		}
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
		f.view(func(tx store.ReadTx) error { obligationInvariants(t, tx, i, frozen); return nil })
	}
	return out
}

// transitionStep builds a generated transition against the current
// obligations, or ok=false when there are none yet.
func (f *fixture) transitionStep(g genTransition) (domain.Principal, domain.Event, bool) {
	var cur []domain.ObligationVersion
	f.view(func(tx store.ReadTx) error {
		all, err := tx.Obligations("T")
		for _, o := range all {
			if o.Current {
				cur = append(cur, o)
			}
		}
		return err
	})
	if len(cur) == 0 {
		return domain.Principal{}, domain.Event{}, false
	}
	o := cur[g.pick%len(cur)]
	if g.stale && o.Revision > 1 {
		o.Revision--
	}
	mode := domain.AssertionMode("")
	if g.to == domain.ObligationSatisfied {
		mode = domain.AssertionAttestation
	}
	kind := map[domain.Authority]domain.EventKind{domain.AuthoritySystem: domain.EventSystem, domain.AuthorityHarness: domain.EventHarness, domain.AuthorityUser: domain.EventUser}[g.authority]
	return principal(g.authority), transitionEvent(g.id, kind, o, g.to, mode), true
}

// obligationInvariants checks the obligation half after a step: at most one
// current version per obligation, whose source is the current directive;
// SATISFIED always rests on an assertion or proof (INV-16); WAIVED is
// terminal and retired versions never change afterwards; every recorded
// transition was made at or above its source's authority or under a grant
// (INV-04).
func obligationInvariants(t *testing.T, tx store.ReadTx, step int, frozen map[string]domain.ObligationStatus) {
	t.Helper()
	latest, err := tx.Obligations("T")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range latest {
		versions, err := tx.ObligationVersions(l.ObligationID)
		if err != nil {
			t.Fatal(err)
		}
		current := 0
		for _, o := range versions {
			key := fmt.Sprintf("%s/%d", o.ObligationID, o.Version)
			if prev, ok := frozen[key]; ok && prev != o.Status {
				t.Fatalf("step %d: frozen obligation %s changed %s -> %s", step, key, prev, o.Status)
			}
			if !o.Current || o.Status == domain.ObligationWaived {
				frozen[key] = o.Status
			}
			if o.Status == domain.ObligationSatisfied && o.CurrentAssertionID == "" && o.CurrentProofID == "" {
				t.Fatalf("step %d: %s SATISFIED without assertion or proof", step, key)
			}
			if !o.Current {
				continue
			}
			current++
			if ok, err := graph.IsCurrent(tx, o.SourceItemID); err != nil || !ok {
				t.Fatalf("step %d: current obligation %s has a noncurrent source (%v)", step, key, err)
			}
		}
		if current > 1 {
			t.Fatalf("step %d: %s has %d current versions", step, l.ObligationID, current)
		}
		trs, err := tx.ObligationTransitions(l.ObligationID)
		if err != nil {
			t.Fatal(err)
		}
		for _, tr := range trs {
			for _, o := range versions {
				if o.Version == tr.Version && !tr.Actor.Authority.AtLeast(o.SourceAuthority) && tr.GrantID == "" {
					t.Fatalf("step %d: %s-authority transition on a %s source without a grant: %+v", step, tr.Actor.Authority, o.SourceAuthority, tr)
				}
			}
		}
	}
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
// committed event afterwards replays its receipt without side effects. It
// runs on both backends (TEST-1.6), SQLite with fewer, parallel seeds for runtime,
// and a SQLite history must also replay identically into the memory store.
func TestProperty_GeneratedHistories(t *testing.T) {
	for _, b := range []struct {
		name  string
		open  propertyStore
		seeds int
	}{{"memory", memoryPropertyStore, 24}, {"sqlite", sqlitePropertyStore, 6}} {
		t.Run(b.name, func(t *testing.T) {
			seeds := b.seeds
			if testing.Short() {
				seeds = min(seeds, 4)
			}
			for seed := range uint64(seeds) {
				t.Run(fmt.Sprint(seed), func(t *testing.T) {
					// SQLite histories are commit-bound, not CPU-bound.
					if b.name == "sqlite" {
						t.Parallel()
					}
					checkGeneratedHistory(t, genHistory(seed, 40), b.open, b.name == "sqlite")
				})
			}
		})
	}
}

// checkGeneratedHistory runs h on a fresh store from open, replays it into
// another (and, crossBackend, into a memory store too), and checks INV-09.
func checkGeneratedHistory(t *testing.T, h []genStep, open propertyStore, crossBackend bool) {
	f := newPropertyFixture(t, open)
	got := runHistory(t, f, h)

	replays := []*fixture{newPropertyFixture(t, open)}
	if crossBackend {
		replays = append(replays, newPropertyFixture(t, memoryPropertyStore))
	}
	for _, g := range replays {
		replay := runHistory(t, g, h)
		for i := range got {
			if (got[i] == nil) != (replay[i] == nil) || got[i] != nil && !reflect.DeepEqual(normReceipt(*got[i]), normReceipt(*replay[i])) {
				t.Fatalf("step %d: replay diverged", i)
			}
		}
		if !reflect.DeepEqual(backendNeutral(snapshotPhase2(t, f.s)), backendNeutral(snapshotPhase2(t, g.s))) {
			t.Fatalf("replayed state diverged")
		}
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
}

// backendNeutral is v as generic JSON with empty lists folded to null: the
// backends differ only in returning an empty or a nil slice for no rows,
// which is not a state difference.
func backendNeutral(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	var fold func(any) any
	fold = func(v any) any {
		switch x := v.(type) {
		case []any:
			if len(x) == 0 {
				return nil
			}
			for i := range x {
				x[i] = fold(x[i])
			}
		case map[string]any:
			for k := range x {
				x[k] = fold(x[k])
			}
		}
		return v
	}
	return fold(out)
}
