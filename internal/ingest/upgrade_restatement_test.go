package ingest

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
	"github.com/tdavison784/context-runtime/internal/tools"
)

// restatement is one section of the upgraded fixture's current directives,
// rebuilt verbatim from their recorded source ranges, with the original
// event's principal and span, and the prior version of each of its lines.
type restatement struct {
	p      domain.Principal
	e      domain.Event
	priors map[string]string // directive ID -> prior (current) item ID
}

// currentDirectiveRestatements derives, from the store alone, one event per
// (originating event, section) that restates every current directive item
// in it verbatim: the section heading and the exact source line of each
// item, in source order, under the original principal, event kind and span
// (authority, boundary, capability). Nothing is hand-listed, so every
// current pre-upgrade directive is covered.
func currentDirectiveRestatements(t *testing.T, f *fixture) []restatement {
	t.Helper()
	type line struct {
		start     int
		text      string
		id, dirID string
	}
	type group struct {
		eventID, header string
		transcript      string
		part            int
		lines           []line
	}
	groups := map[string]*group{}
	var order []string
	var out []restatement
	f.view(func(tx store.ReadTx) error {
		items, err := tx.Items(store.ItemFilter{})
		if err != nil {
			return err
		}
		byID := map[string]domain.ContextItem{}
		for _, it := range items {
			byID[it.ID] = it
		}
		for _, it := range items {
			if it.DirectiveID == "" {
				continue
			}
			if cur, err := graph.IsCurrent(tx, it.ID); err != nil || !cur {
				continue
			}
			if len(it.SourceRanges) != 1 {
				t.Fatalf("[%s] %s has %d source ranges", it.DirectiveID, it.ID, len(it.SourceRanges))
			}
			sr := it.SourceRanges[0]
			text := byID[sr.TranscriptID].Parts[sr.PartIndex].Text
			src := text[sr.Range.Start:sr.Range.End]
			header := ""
			if !strings.HasPrefix(src, "## ") {
				h := strings.LastIndex("\n"+text[:sr.Range.Start], "\n## ")
				if h < 0 {
					t.Fatalf("[%s] has no section heading", it.DirectiveID)
				}
				end := strings.IndexByte(text[h:], '\n')
				header = text[h : h+end+1]
			}
			key := it.EventID + "\x00" + header + "\x00" + sr.TranscriptID
			g, ok := groups[key]
			if !ok {
				g = &group{eventID: it.EventID, header: header, transcript: sr.TranscriptID, part: sr.PartIndex}
				groups[key] = g
				order = append(order, key)
			}
			g.lines = append(g.lines, line{sr.Range.Start, src, it.ID, it.DirectiveID})
		}
		for n, key := range order {
			g := groups[key]
			sort.Slice(g.lines, func(i, j int) bool { return g.lines[i].start < g.lines[j].start })
			env, err := tx.Envelope(domain.CallerOccurrenceID(sess, g.eventID))
			if err != nil {
				return err
			}
			var span *domain.Span
			for si := range env.Event.Spans {
				s := env.Event.Spans[si]
				if g.part < len(s.Parts) && s.Parts[g.part].Text == byID[g.transcript].Parts[g.part].Text {
					span = &s
					break
				}
			}
			if span == nil {
				t.Fatalf("event %s: no span matches the transcript", g.eventID)
			}
			body := g.header
			r := restatement{p: env.Principal, priors: map[string]string{}}
			for _, l := range g.lines {
				text := l.text
				if !strings.HasSuffix(text, "\n") {
					text += "\n"
				}
				body += text
				r.priors[l.dirID] = l.id
			}
			s := *span
			s.Parts = []domain.InputPart{{Type: domain.PartText, MediaType: s.Parts[g.part].MediaType, Text: body}}
			r.e = domain.Event{EventID: fmt.Sprintf("g5-restate-%d", n), Kind: env.Event.Kind, Spans: []domain.Span{s}}
			out = append(out, r)
		}
		return nil
	})
	return out
}

// TestUpgradeRestatesEveryCurrentDirective_G5 (ruling 4; G5, SPEC-1.3,
// FROZEN C-1, P3-4/41): after the frozen Phase 2 database is upgraded
// (migration 0034 reconciles creation declarations), restating every
// current pre-upgrade directive verbatim, as derived from the store, is a
// duplicate of exactly its prior. Nothing is replaced or re-pinned, every
// prior stays current, and no obligation, transition or grant changes.
func TestUpgradeRestatesEveryCurrentDirective_G5(t *testing.T) {
	s := openPhase2Copy(t)
	if !hasSemantic(s) {
		t.Skip("GATE-PENDING: needs " + depW2)
	}
	f := newFixture(t, s)
	before := snapshotPhase2(t, s)
	rs := currentDirectiveRestatements(t, f)
	var restated int
	for _, r := range rs {
		rc, err := f.ingest(r.p, r.e)
		if err != nil {
			t.Errorf("%s (%q): %v", r.e.EventID, r.e.Spans[0].Parts[0].Text, err)
			continue
		}
		if len(rc.Replacements) != 0 {
			t.Errorf("%s: identical restatement replaced %+v", r.e.EventID, rc.Replacements)
		}
		dups := map[string]string{}
		for _, l := range rc.Duplicates {
			dups[l.ItemID] = l.TargetID
		}
		for _, it := range semantic(rc) {
			if it.DirectiveID == "" {
				continue
			}
			restated++
			want := r.priors[it.DirectiveID]
			if got, ok := dups[it.ID]; !ok || got != want {
				t.Errorf("%s: [%s] duplicate of %q, want %q", r.e.EventID, it.DirectiveID, got, want)
			}
			if !f.isCurrent(want) {
				t.Errorf("%s: [%s] prior %s is no longer current", r.e.EventID, it.DirectiveID, want)
			}
		}
	}
	var current int
	for _, r := range rs {
		current += len(r.priors)
	}
	if restated != current || current == 0 {
		t.Fatalf("restated %d directive items of %d current", restated, current)
	}
	t.Logf("restated %d current pre-upgrade directive items in %d events", restated, len(rs))
	after := snapshotPhase2(t, s)
	if !reflect.DeepEqual(after.Versions, before.Versions) || !reflect.DeepEqual(after.Transitions, before.Transitions) || !reflect.DeepEqual(after.Grants, before.Grants) {
		t.Errorf("identical restatement changed obligations or grants")
	}
}

// TestUpgradeAgentOwnOldKey_G5 (ruling 4; G5, SPEC-1.3, SPEC-1.5, P3-41): an
// agent's pre-upgrade key, in the form migration 0034 leaves it (no
// explicit namespace, an unknown creation declaration), on the upgraded
// Phase 2 database. Through the real round and tool pipeline, the owning
// agent's identical restatement can neither dedup against unknown identity
// nor rebind the key: it fails closed with nothing written, and the old
// version stays current. A changed update by the same agent replaces its
// own old key (SPEC-1.5) with an explicit AGENT_KEY version.
func TestUpgradeAgentOwnOldKey_G5(t *testing.T) {
	s := openPhase2Copy(t)
	if !hasSemantic(s) {
		t.Skip("GATE-PENDING: needs " + depW2)
	}
	f := newFixture(t, s)
	agent := agentPrincipal()
	dispatcher := dispatcherFor(agent)
	b := f.inference(agent, "r1")
	if _, err := f.in.IngestOutcome(ctx, f.s, b, outcomeEvent(b, "Updating my status."), &OutcomeMembership{Dispatcher: dispatcher, ToolCallIDs: []string{"probe", "same", "changed"}}); err != nil {
		t.Fatal(err)
	}
	svc, err := tools.NewService(policy.DefaultPhase3Policy())
	if err != nil {
		t.Fatal(err)
	}
	updateState := func(call, key, text string) (domain.ToolResult, error) {
		inv := domain.ToolInvocation{SessionID: sess, Principal: agent, ConversationID: b.ConversationID, ExchangeID: b.ExchangeID, CallID: b.CallID, ToolCallID: call, TurnID: b.TurnID}
		return tools.Execute(ctx, f.s, sess, func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
			return svc.UpdateState(tx, dispatcher, tools.Request[domain.KeyedWriteIntent]{Invocation: inv,
				Intent: domain.KeyedWriteIntent{RequestID: "req-" + call, Key: key, Kind: domain.KindTaskState, Parts: textParts(text)}}, seq)
		})
	}
	// A tool-written key gives the exact row shape of an agent key; the
	// legacy key is that shape under another key, as 0034 leaves it.
	probe, err := updateState("probe", "probe", "Working on the API.")
	if err != nil {
		t.Fatal(err)
	}
	legacy := f.item(probe.Keyed.ItemID)
	legacy.DirectiveID, legacy.Namespace = domain.AgentKeyID("status"), ""
	if err := f.s.Update(ctx, sess, func(tx store.Tx) error {
		legacy.ID, legacy.Seq = "itm_legacy_agent_status", tx.NextSeq()
		if err := tx.InsertItem(legacy); err != nil {
			return err
		}
		if err := storetest.UncheckedSetCurrentVersion(tx, legacy.ID); err != nil {
			return err
		}
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		return sem.InsertCreationDeclaration(domain.CreationDeclaration{SemanticMeta: domain.SemanticMeta{ID: "decl_legacy_agent_status", SessionID: sess,
			SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()}, ItemID: legacy.ID, PolicyVersion: "legacy-creation-reconciliation/v1"})
	}); err != nil {
		t.Fatal(err)
	}
	if !f.isCurrent(legacy.ID) {
		t.Fatal("legacy agent key is not current")
	}

	before := snapshotPhase2(t, f.s)
	// graph.ErrUnknownDeclaration (domain.ErrUnsupportedSchema) reaches the
	// model as the fixed UNAVAILABLE tool error; the changed update below
	// succeeding on the same key shows the refusal is the identity check.
	var te *tools.Error
	if _, err := updateState("same", "status", "Working on the API."); !errors.As(err, &te) || te.Code() != domain.ToolErrorUnavailable {
		t.Fatalf("identical restatement of an unknown-identity agent key: %v, want the fail-closed UNAVAILABLE refusal", err)
	}
	if after := snapshotPhase2(t, f.s); !reflect.DeepEqual(normGolden(after), normGolden(before)) || !f.isCurrent(legacy.ID) {
		t.Fatal("the refused restatement changed state or rebound the key")
	}

	changed, err := updateState("changed", "status", "API done; running tests.")
	if err != nil {
		t.Fatalf("the owning agent cannot update its own old key: %v", err)
	}
	if changed.Keyed == nil || changed.Keyed.Duplicate || changed.Keyed.SupersededItemID != legacy.ID || f.isCurrent(legacy.ID) || !f.isCurrent(changed.Keyed.ItemID) {
		t.Fatalf("changed update: %+v", changed.Keyed)
	}
	if it := f.item(changed.Keyed.ItemID); it.Namespace != domain.NamespaceAgentKey {
		t.Fatalf("new version namespace = %q", it.Namespace)
	}
}
