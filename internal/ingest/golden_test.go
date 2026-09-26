package ingest

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"unicode/utf8"

	"github.com/tdavison784/context-runtime/internal/directive"
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// The canonical directive examples (testdata/directives, FR-DIR-006) are
// also driven through Ingest on BOTH stores. Each example's
// expected-ingest.json records the ingestion outcome of its event, and both
// stores must produce it byte for byte (store parity, FR-PER-003). Regenerate
// with -update and review every change by hand; -update records behavior,
// it does not validate it. expected.json stays the parser-only golden.
var updateIngest = flag.Bool("update", false, "rewrite testdata/directives/*/expected-ingest.json")

const exampleRoot = "../../testdata/directives"

// exampleUnit mirrors the unit.json schema read by the directive golden test.
type exampleUnit struct {
	Description      string           `json:"description"`
	Requirements     []string         `json:"requirements"`
	Input            string           `json:"input"`
	Authority        domain.Authority `json:"authority"`
	DirectiveCapable bool             `json:"directive_capable"`
	SpanIndex        int              `json:"span_index"`
	PartIndex        int              `json:"part_index"`
	NoDirectives     bool             `json:"no_directives"`
	MoreUnits        []exampleUnit    `json:"more_units"`
}

// exampleEvent builds one event from an example: units sharing a span index
// are that span's text parts in part order. The event kind and principal
// authority are the highest unit authority; every span has the task boundary.
func exampleEvent(t *testing.T, dir, name string) (domain.Principal, domain.Event, exampleUnit) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "unit.json"))
	if err != nil {
		t.Fatal(err)
	}
	var u exampleUnit
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatal(err)
	}
	if u.Input == "" {
		u.Input = "input.md"
	}
	units := append([]exampleUnit{u}, u.MoreUnits...)
	kind := u.Authority
	var spans []domain.Span
	for _, unit := range units {
		text, err := os.ReadFile(filepath.Join(dir, unit.Input))
		if err != nil {
			t.Fatal(err)
		}
		for len(spans) <= unit.SpanIndex {
			spans = append(spans, domain.Span{Authority: unit.Authority, Access: taskAccess(), DirectiveCapable: unit.DirectiveCapable})
		}
		s := &spans[unit.SpanIndex]
		if s.Authority != unit.Authority || s.DirectiveCapable != unit.DirectiveCapable || len(s.Parts) != unit.PartIndex {
			t.Fatalf("units of span %d disagree or are out of part order", unit.SpanIndex)
		}
		s.Parts = append(s.Parts, domain.InputPart{Type: domain.PartText, MediaType: "text/markdown", Text: string(text)})
		if unit.Authority.AtLeast(kind) {
			kind = unit.Authority
		} else if !kind.AtLeast(unit.Authority) {
			t.Fatalf("incomparable unit authorities %s and %s", kind, unit.Authority)
		}
	}
	return principal(kind), domain.Event{EventID: "ex-" + name, Kind: domain.EventKind(kind), Spans: spans}, u
}

// setupEvent opens turn 1 so TURN-scoped and TTL items have an owning turn
// whatever the example's own event kind.
func setupEvent() domain.Event { return userEvent("setup", "start", false) }

type ingestGolden struct {
	Error         string             `json:"error,omitempty"`
	Seq           uint64             `json:"seq,omitempty"`
	OpenedTurn    uint64             `json:"opened_turn,omitempty"`
	Items         []goldenItem       `json:"items"`
	Relationships []goldenRelation   `json:"relationships"`
	Obligations   []goldenObligation `json:"obligations"`
	Lifecycle     []goldenLifecycle  `json:"lifecycle"`
	Diagnostics   []goldenDiagnostic `json:"diagnostics"`
	Duplicates    [][2]string        `json:"duplicates,omitempty"`
	Replacements  [][2]string        `json:"replacements,omitempty"`
}

type goldenAccess struct {
	Scope      domain.Scope `json:"scope"`
	WorkflowID string       `json:"workflow_id,omitempty"`
	TaskID     string       `json:"task_id,omitempty"`
	AgentID    string       `json:"agent_id,omitempty"`
}

type goldenRange struct {
	Transcript string   `json:"transcript"`
	Part       int      `json:"part"`
	Range      [2]int   `json:"range"`
	Slices     [][2]int `json:"slices,omitempty"`
}

type goldenItem struct {
	Ref          string                  `json:"ref"`
	ID           string                  `json:"id"`
	Role         domain.ItemRole         `json:"role,omitempty"`
	Kind         domain.Kind             `json:"kind"`
	Section      domain.DirectiveSection `json:"section,omitempty"`
	DirectiveID  string                  `json:"directive_id,omitempty"`
	Generation   domain.Generation       `json:"generation"`
	Authority    domain.Authority        `json:"authority"`
	Scope        domain.Scope            `json:"scope"`
	Access       goldenAccess            `json:"access"`
	Residency    domain.Residency        `json:"residency"`
	Retention    domain.RetentionClass   `json:"retention"`
	GoalStatus   domain.GoalStatus       `json:"goal_status,omitempty"`
	TTLTurns     int                     `json:"ttl_turns,omitempty"`
	CreatedTurn  uint64                  `json:"created_turn,omitempty"`
	HasTurnID    bool                    `json:"has_turn_id,omitempty"`
	Parts        []goldenPart            `json:"parts"`
	ContentHash  string                  `json:"content_hash"`
	SourceRanges []goldenRange           `json:"source_ranges,omitempty"`
}

type goldenPart struct {
	Text    string `json:"text,omitempty"`
	TextHex string `json:"text_hex,omitempty"`
}

type goldenRelation struct {
	Type        domain.RelationshipType `json:"type"`
	From        string                  `json:"from"`
	To          string                  `json:"to"`
	Authority   domain.Authority        `json:"authority"`
	RuleVersion string                  `json:"rule_version,omitempty"`
	Coverage    []string                `json:"coverage,omitempty"`
}

type goldenObligation struct {
	Source          string                  `json:"source"`
	Version         uint64                  `json:"version"`
	Claim           string                  `json:"claim"`
	Status          domain.ObligationStatus `json:"status"`
	Current         bool                    `json:"current"`
	SourceAuthority domain.Authority        `json:"source_authority"`
	Access          goldenAccess            `json:"access"`
	HasMatcher      bool                    `json:"has_matcher,omitempty"`
	Retired         bool                    `json:"retired,omitempty"`
}

type goldenLifecycle struct {
	Action     domain.LifecycleAction  `json:"action"`
	TargetID   string                  `json:"target_id"`
	Authority  domain.Authority        `json:"authority"`
	Actor      domain.Authority        `json:"actor_authority"`
	Status     domain.CommandStatus    `json:"status"`
	Resolution domain.TargetResolution `json:"resolution"`
	Resolved   string                  `json:"resolved,omitempty"`
	Range      [2]int                  `json:"range"`
}

type goldenDiagnostic struct {
	Span        int                     `json:"span"`
	Part        int                     `json:"part"`
	Index       int                     `json:"index"`
	Code        domain.DiagnosticCode   `json:"code"`
	Reason      domain.DiagnosticReason `json:"reason,omitempty"`
	Section     string                  `json:"section,omitempty"`
	DirectiveID string                  `json:"directive_id,omitempty"`
	Range       [2]int                  `json:"range"`
	Access      domain.Scope            `json:"access_scope"`
}

func access(b domain.AccessBoundary) goldenAccess {
	return goldenAccess{b.Scope, b.WorkflowID, b.TaskID, b.AgentID}
}

func pair(r domain.ByteRange) [2]int { return [2]int{r.Start, r.End} }

// classify maps a rejected event to a stable golden label; an unexpected
// error class fails the test rather than being recorded.
func classify(t *testing.T, err error) string {
	switch {
	case errors.Is(err, directive.ErrRepresentationLimit):
		return "representation_limit"
	case errors.Is(err, domain.ErrInvalidAuthorityPromotion):
		return "invalid_authority_promotion"
	case errors.Is(err, domain.ErrInvalidRecord):
		return "invalid_record"
	}
	t.Fatalf("unexpected ingest error: %v", err)
	return ""
}

// project renders the receipt of an example event plus the relationships
// and obligations it created. Item IDs become refs: "event#i" for the
// receipt's i-th item and "setup#i" for the setup event's.
func project(t *testing.T, f *fixture, setup, r domain.IngestReceipt, base uint64) ingestGolden {
	refs := map[string]string{}
	for i, it := range setup.Items {
		refs[it.ID] = fmt.Sprintf("setup#%d", i)
	}
	for i, it := range r.Items {
		refs[it.ID] = fmt.Sprintf("event#%d", i)
	}
	ref := func(id string) string {
		if s, ok := refs[id]; ok {
			return s
		}
		t.Fatalf("golden references an item outside the example: %s", id)
		return ""
	}
	g := ingestGolden{Seq: r.Seq, OpenedTurn: r.OpenedTurn, Items: []goldenItem{}, Relationships: []goldenRelation{}, Obligations: []goldenObligation{}, Lifecycle: []goldenLifecycle{}, Diagnostics: []goldenDiagnostic{}}
	for _, it := range r.Items {
		gi := goldenItem{Ref: ref(it.ID), ID: it.ID, Role: it.Role, Kind: it.Kind, Section: it.Section, DirectiveID: it.DirectiveID, Generation: it.Generation,
			Authority: it.Authority, Scope: it.Scope, Access: access(it.Access), Residency: it.Residency, Retention: it.Retention,
			CreatedTurn: it.CreatedTurn, HasTurnID: it.TurnID != "", ContentHash: it.ContentHash}
		if it.GoalStatus != nil {
			gi.GoalStatus = *it.GoalStatus
		}
		if it.TTLTurns != nil {
			gi.TTLTurns = *it.TTLTurns
		}
		for _, part := range it.Parts {
			if part.Type != domain.PartText {
				t.Fatalf("example items are text: %+v", part)
			}
			// Text when valid UTF-8, hex otherwise, so goldens stay lossless (D3).
			if utf8.ValidString(part.Text) {
				gi.Parts = append(gi.Parts, goldenPart{Text: part.Text})
			} else {
				gi.Parts = append(gi.Parts, goldenPart{TextHex: hex.EncodeToString([]byte(part.Text))})
			}
		}
		for _, sr := range it.SourceRanges {
			gr := goldenRange{Transcript: ref(sr.TranscriptID), Part: sr.PartIndex, Range: pair(sr.Range)}
			for _, s := range sr.Slices {
				gr.Slices = append(gr.Slices, pair(s))
			}
			gi.SourceRanges = append(gi.SourceRanges, gr)
		}
		g.Items = append(g.Items, gi)
	}
	f.view(func(tx store.ReadTx) error {
		rels, err := tx.Relationships(store.RelationshipFilter{})
		if err != nil {
			return err
		}
		for _, rel := range rels {
			if rel.Seq <= base {
				continue
			}
			gr := goldenRelation{Type: rel.Type, From: ref(rel.FromID), To: ref(rel.ToID), Authority: rel.Authority, RuleVersion: rel.RuleVersion}
			if rel.Coverage != nil {
				for _, id := range rel.Coverage.ItemIDs {
					gr.Coverage = append(gr.Coverage, ref(id))
				}
			}
			g.Relationships = append(g.Relationships, gr)
		}
		obs, err := tx.Obligations("")
		if err != nil {
			return err
		}
		for _, ob := range obs {
			g.Obligations = append(g.Obligations, goldenObligation{Source: ref(ob.SourceItemID), Version: ob.Version, Claim: ob.Claim, Status: ob.Status, Current: ob.Current,
				SourceAuthority: ob.SourceAuthority, Access: access(ob.Access), HasMatcher: ob.Matcher != nil, Retired: ob.RetiredSeq != 0})
		}
		return nil
	})
	for _, c := range r.Lifecycle {
		gl := goldenLifecycle{Action: c.Action, TargetID: c.TargetID, Authority: c.Authority, Actor: c.Actor.Authority, Status: c.Status, Resolution: c.Resolution, Range: pair(c.Range)}
		if c.ResolvedItemID != "" {
			gl.Resolved = ref(c.ResolvedItemID)
		}
		g.Lifecycle = append(g.Lifecycle, gl)
	}
	for _, d := range r.Diagnostics {
		g.Diagnostics = append(g.Diagnostics, goldenDiagnostic{d.SpanIndex, d.PartIndex, d.Index, d.Code, d.Reason, d.Section, d.DirectiveID, pair(d.Range), d.Access.Scope})
	}
	for _, l := range r.Duplicates {
		g.Duplicates = append(g.Duplicates, [2]string{ref(l.ItemID), ref(l.TargetID)})
	}
	for _, l := range r.Replacements {
		g.Replacements = append(g.Replacements, [2]string{ref(l.ItemID), ref(l.TargetID)})
	}
	return g
}

// runExample ingests setup then the example event and returns the encoded
// golden. It also checks invariants no golden may override.
func runExample(t *testing.T, f *fixture, dir, name string) []byte {
	p, e, u := exampleEvent(t, dir, name)
	setup := f.mustIngest(principal(domain.AuthorityUser), setupEvent())
	base := f.lastSeq()
	var g ingestGolden
	r, err := f.ingest(p, e)
	if err != nil {
		g = ingestGolden{Error: classify(t, err), Items: []goldenItem{}, Relationships: []goldenRelation{}, Obligations: []goldenObligation{}, Lifecycle: []goldenLifecycle{}, Diagnostics: []goldenDiagnostic{}}
		if f.lastSeq() != base {
			t.Fatalf("rejected event allocated sequence numbers")
		}
	} else {
		g = project(t, f, setup, r, base)
		for _, it := range r.Items {
			if !e.Kind.Authority().AtLeast(it.Authority) {
				t.Fatalf("item authority %s exceeds event %s", it.Authority, e.Kind)
			}
			// Injection examples may yield transcripts and, for trusted
			// spans, residual instruction items (D8), never directive items.
			if u.NoDirectives && it.Section != domain.SectionNone {
				t.Fatalf("injection example created a directive item: %+v", it)
			}
		}
		if u.NoDirectives && (len(r.Lifecycle) != 0 || len(g.Obligations) != 0) {
			t.Fatal("injection example created lifecycle commands or obligations")
		}
		// Retry identity on every example: same request, same receipt, no seq.
		seq := f.lastSeq()
		again, err := f.ingest(p, e)
		if err != nil || !slices.Equal(again.ItemIDs(), r.ItemIDs()) || again.Seq != r.Seq || f.lastSeq() != seq {
			t.Fatalf("retry changed the outcome: %v", err)
		}
	}
	out, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func TestCanonicalExamplesThroughIngest(t *testing.T) {
	entries, err := os.ReadDir(exampleRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name, dir := entry.Name(), filepath.Join(exampleRoot, entry.Name())
		t.Run(name, func(t *testing.T) {
			var outputs [][]byte
			eachStore(t, func(t *testing.T, f *fixture) {
				outputs = append(outputs, runExample(t, f, dir, name))
			})
			if t.Failed() {
				return
			}
			if len(outputs) != 2 || !bytes.Equal(outputs[0], outputs[1]) {
				t.Fatalf("stores disagree:\nmemory:\n%s\nsqlite:\n%s", outputs[0], outputs[1])
			}
			path := filepath.Join(dir, "expected-ingest.json")
			if *updateIngest {
				if err := os.WriteFile(path, outputs[0], 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/ingest -run TestCanonicalExamplesThroughIngest -update and review)", err)
			}
			if !bytes.Equal(want, outputs[0]) {
				t.Fatalf("ingest golden mismatch for %s; got:\n%s", name, outputs[0])
			}
		})
	}
}
