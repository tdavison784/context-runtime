package ingest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
)

// testdata/phase2/reserved_shaped.db was written by the Phase 2 binary
// (main, b869cb1) from shapedLegacyEvents, which Phase 2 accepted as plain
// events although their EventIDs have the exact shape the outcome path
// derives today; reserved_shaped.golden.json holds each receipt as that
// binary returned it. Only copies are ever opened.

type legacyStep struct {
	p domain.Principal
	e domain.Event
}

func shapedLegacyEvents() []legacyStep {
	user, agent := principal(domain.AuthorityUser), principal(domain.AuthorityAgent)
	shaped := "outcome-" + strings.Repeat("a", 64)
	return []legacyStep{
		{user, userEvent("plain-start", "hello", false)},
		{user, userEvent(shaped, "a runtime-shaped outcome name", false)},
		{user, userEvent(shaped+"/call_1", "a runtime-shaped tool outcome name", false)},
		{agent, domain.Event{EventID: "outcome-" + strings.Repeat("b", 64), Kind: domain.EventAgent, Spans: []domain.Span{textSpan(domain.AuthorityAgent, false, "an agent's shaped note")}}},
	}
}

// TestRuntimeShapedPhase2ReceiptsReplay_SEC49 (SEC-4.9, DUR-2.8, SEC-3.3):
// a Phase 2 plain event whose EventID has the exact shape of a runtime
// outcome ID still replays its receipt verbatim for its own principal,
// allocating nothing; a changed request by its owner conflicts. Any other
// principal gets the same bare invalid record for it as for an absent ID of
// that shape.
func TestRuntimeShapedPhase2ReceiptsReplay_SEC49(t *testing.T) {
	var golden map[string]domain.IngestReceipt
	b, err := os.ReadFile(filepath.Join(phase2Dir, "reserved_shaped.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "reserved_shaped.db")
	copyFile(t, filepath.Join(phase2Dir, "reserved_shaped.db"), path)
	s, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open frozen runtime-shaped database: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	f := newFixture(t, s)
	seq := f.lastSeq()
	for _, st := range shapedLegacyEvents() {
		got, err := f.ingest(st.p, st.e)
		if err != nil || !reflect.DeepEqual(normReceipt(got), normReceipt(golden[st.e.EventID])) {
			t.Errorf("%s: exact retry by its owner = %v", st.e.EventID, err)
		}
	}
	if f.lastSeq() != seq {
		t.Fatalf("retries allocated sequences: %d -> %d", seq, f.lastSeq())
	}
	owner := shapedLegacyEvents()[1]
	changed := owner.e
	changed.Spans = []domain.Span{textSpan(domain.AuthorityUser, false, "different")}
	if _, err := f.ingest(owner.p, changed); !errors.Is(err, domain.ErrEventIDConflict) {
		t.Errorf("changed request by the owner = %v, want conflict", err)
	}
	other := principal(domain.AuthorityUser)
	other.AgentID = "B"
	absent := "outcome-" + strings.Repeat("c", 64)
	for _, st := range append(shapedLegacyEvents()[1:], legacyStep{other, userEvent(absent, "x", false)}, legacyStep{other, userEvent(absent+"/call_9", "x", false)}) {
		e := st.e
		if e.Kind != domain.EventUser {
			e = userEvent(e.EventID, "probe", false)
		}
		if _, err := f.ingest(other, e); !errors.Is(err, domain.ErrInvalidRecord) || errors.Is(err, domain.ErrEventIDConflict) {
			t.Errorf("%s by another principal = %v, want the uniform invalid record", e.EventID, err)
		}
	}
}

// TestOutcomeOwnerCannotProbeOwnOutcome_SEC49 (SEC-4.9, SEC-3.3, G3): the
// agent that owns a call cannot use the plain path to learn whether its own
// outcome was recorded. Before and after IngestOutcome, a plain event under
// the outcome's EventID, with the recorded content or any other, gets the
// same bare invalid record, and writes nothing.
func TestOutcomeOwnerCannotProbeOwnOutcome_SEC49(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		f.mustIngest(principal(domain.AuthorityUser), userEvent("q", "Run the tests.", false))
		agent := agentPrincipal()
		b := f.inference(agent, "r1")
		out := outcomeEvent(b, "done")
		probe := func() []error {
			var errs []error
			for _, e := range []domain.Event{out, outcomeEvent(b, "something else")} {
				before := f.lastSeq()
				_, err := f.ingest(agent, e)
				if f.lastSeq() != before {
					t.Fatalf("plain probe wrote")
				}
				errs = append(errs, err)
			}
			return errs
		}
		before := probe()
		if _, err := f.in.IngestOutcome(ctx, f.s, b, out, &OutcomeMembership{Dispatcher: dispatcherFor(agent)}); err != nil {
			t.Fatalf("outcome: %v", err)
		}
		after := probe()
		for i := range before {
			if !errors.Is(before[i], domain.ErrInvalidRecord) || after[i] == nil || before[i].Error() != after[i].Error() {
				t.Errorf("ORACLE: owner probe %d before=%v after=%v", i, before[i], after[i])
			}
		}
	})
}
