//go:build ignore

// Command gen writes the frozen Phase 2 SQLite fixture (phase2.db) and the
// logical state its binary read back (manifest.json). It builds only
// against the Phase 2 tree it was frozen at (commit b5f6b1f, the merged
// Phase 2 base): run it from a checkout of that commit, never from a later
// tree, because the fixture must be what the old binary wrote.
//
//	git archive b5f6b1f | tar -x -C /tmp/p2
//	cp gen.go /tmp/p2/internal/store/sqlite/testdata/phase2/
//	cd /tmp/p2 && go run ./internal/store/sqlite/testdata/phase2/gen.go -out DIR
//
// The fixture exercises every Phase 2 record family an upgrade must
// preserve: caller-keyed and anonymous ingestion receipts and envelopes,
// parsed-but-unexecuted Resolve/Unpin commands, pinned directives with
// obligation claims (unbound), Goal/Working/Remember/References/Ephemeral
// sections, unresolved references, an image blob, invalid UTF-8 and NUL
// bytes, legacy occurrence and stable-ID obligation grants, a USER-asserted
// SATISFIED obligation, a matcher-SATISFIED obligation (for upgrade
// reconciliation), a BLOCKED obligation, a call in flight with its attempt,
// and a second session.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/ingest"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

var ctx = context.Background()

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func principal(sess string, a domain.Authority) domain.Principal {
	return domain.Principal{SessionID: sess, WorkflowID: "W", TaskID: "T", AgentID: "A", Authority: a}
}

func span(sess string, a domain.Authority, capable bool, parts ...domain.InputPart) domain.Span {
	return domain.Span{Authority: a, Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: "T"}, DirectiveCapable: capable, Parts: parts}
}

func text(s string) domain.InputPart {
	return domain.InputPart{Type: domain.PartText, MediaType: "text/markdown", Text: s}
}

const directives = "## Goal [upgrade]\nUpgrade Foo to v2 while maintaining backwards compatibility.\n\n" +
	"## Pinned\n- [api] Do not modify exported APIs.\n- [tests] {obligation=tests_pass} All tests must pass.\n" +
	"- [lint] {obligation=lint.clean} Lint is clean.\n- [docs] {obligation=docs.done} Docs are updated.\n\n" +
	"## Working\n- Investigating internal/client.go.\n\n## Remember\n- [retry] {kind=decision} Keep the v1 retry policy.\n\n" +
	"## References\n- docs/architecture.md\n- go.mod\n\n## Ephemeral ttl=2\n- (pasted build output)\n"

type sessionState struct {
	LastSeq       uint64
	Items         []domain.ContextItem
	Relationships []domain.Relationship
	Events        []domain.EventRecord
	Obligations   []domain.ObligationVersion
	Transitions   []domain.ObligationTransition
	Grants        []domain.MutationGrant
	Tasks         []domain.TaskState
	Lifecycle     []domain.LifecycleEvent
	Receipts      []domain.IngestReceipt
	Envelopes     []domain.EventEnvelope
	Diagnostics   []domain.DiagnosticRecord
	Commands      []domain.LifecycleCommandRecord
	Conversations []domain.Conversation
	Calls         []domain.CallRecord
	Attempts      []domain.CallAttempt
}

type manifest struct {
	BaseCommit  string
	Occurrences map[string][]string // session -> occurrence IDs
	Viewers     map[string]domain.Principal
	Sessions    map[string]sessionState
}

func main() {
	out := flag.String("out", "", "output directory")
	flag.Parse()
	if *out == "" {
		must(fmt.Errorf("-out is required"))
	}
	path := filepath.Join(*out, "phase2.db")
	_ = os.Remove(path)
	s, err := sqlite.Open(ctx, path)
	must(err)
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	in := ingest.Ingester{IDs: &domain.SequentialIDs{}, Now: func() time.Time { return now }}
	m := manifest{BaseCommit: "b5f6b1f", Occurrences: map[string][]string{}, Viewers: map[string]domain.Principal{}, Sessions: map[string]sessionState{}}

	ingestAs := func(p domain.Principal, e domain.Event) domain.IngestReceipt {
		r, err := in.Ingest(ctx, s, p, e)
		must(err)
		m.Occurrences[p.SessionID] = append(m.Occurrences[p.SessionID], r.OccurrenceID)
		return r
	}

	const s1, s2 = "s1", "s2"
	user := principal(s1, domain.AuthorityUser)
	harness := principal(s1, domain.AuthorityHarness)
	m.Viewers[s1], m.Viewers[s2] = harness, principal(s2, domain.AuthorityHarness)

	ingestAs(user, domain.Event{EventID: "e-directives", Kind: domain.EventUser, Spans: []domain.Span{span(s1, domain.AuthorityUser, true, text(directives))}})
	ingestAs(user, domain.Event{EventID: "e-commands", Kind: domain.EventUser, Spans: []domain.Span{span(s1, domain.AuthorityUser, true, text("## Resolve [upgrade]\n\n## Unpin [api]\n"))}})
	img := []byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3}
	ingestAs(harness, domain.Event{EventID: "e-tool", Kind: domain.EventTool, Spans: []domain.Span{span(s1, domain.AuthorityTool, false,
		domain.InputPart{Type: domain.PartText, MediaType: "text/plain", Text: "PASS ok\x00 bytes \xff\xfe invalid utf-8"},
		domain.InputPart{Type: domain.PartImage, MediaType: "image/png", Data: img})}})
	ingestAs(harness, domain.Event{Kind: domain.EventHarness, Spans: []domain.Span{span(s1, domain.AuthorityHarness, false, text("anonymous harness note"))}})
	ingestAs(principal(s2, domain.AuthorityUser), domain.Event{EventID: "e-other", Kind: domain.EventUser, Spans: []domain.Span{span(s2, domain.AuthorityUser, true, text("## Pinned\n- [x] Other session.\n"))}})

	// Store-level Phase 2 records the ingestion path does not create.
	must(s.Update(ctx, s1, func(tx store.Tx) error {
		obls, err := tx.Obligations("")
		if err != nil {
			return err
		}
		byClaim := map[string]domain.ObligationVersion{}
		for _, o := range obls {
			byClaim[o.Claim] = o
		}
		goals, err := tx.Items(store.ItemFilter{Kinds: []domain.Kind{domain.KindGoal}})
		if err != nil || len(goals) == 0 {
			return fmt.Errorf("goal: %v", err)
		}
		tools, err := tx.Items(store.ItemFilter{EventID: "e-tool"})
		if err != nil || len(tools) == 0 {
			return fmt.Errorf("tool items: %v", err)
		}
		evidence := tools[0].ID
		system := principal(s1, domain.AuthoritySystem)
		// A bare idempotency record with no replayable envelope: its request
		// schema is unknown after upgrade.
		if _, _, err := tx.InsertEvent(storetest.NewEvent(s1, "e-bare", tx.NextSeq(), "bare payload")); err != nil {
			return err
		}
		// A legacy occurrence grant and a legacy stable-ID matcher grant.
		occ := domain.MutationGrant{ID: "g-item", SessionID: s1, Action: domain.ActionResolve, TargetIDs: []string{goals[0].ID}, Issuer: system, Grantee: &harness, IssuedSeq: tx.NextSeq()}
		if err := tx.InsertGrant(occ); err != nil {
			return err
		}
		tests := byClaim["tests_pass"]
		matcher := &domain.MatcherRef{Name: "tests_pass", Version: "1"}
		mg := domain.MutationGrant{ID: "g-matcher", SessionID: s1, Action: domain.ActionAssertObligation, TargetIDs: []string{tests.ObligationID}, Issuer: system, Matcher: matcher, IssuedSeq: tx.NextSeq()}
		if err := tx.InsertGrant(mg); err != nil {
			return err
		}
		// tests_pass: matcher-derived SATISFIED (reconciled on upgrade).
		if _, err := tx.AppendObligationTransition(domain.ObligationTransition{ID: "tr-tests", SessionID: s1, ObligationID: tests.ObligationID, Version: tests.Version,
			Seq: tx.NextSeq(), From: domain.ObligationUnresolved, To: domain.ObligationSatisfied, Action: domain.ActionAssertObligation, Actor: harness,
			GrantID: mg.ID, Matcher: matcher, EvidenceIDs: []string{evidence}, Fingerprints: []string{domain.HashBytes([]byte("workspace"))}}, tests.Revision); err != nil {
			return err
		}
		// lint.clean: USER-asserted SATISFIED (legacy intent, never reconciled away).
		lint := byClaim["lint.clean"]
		if _, err := tx.AppendObligationTransition(domain.ObligationTransition{ID: "tr-lint", SessionID: s1, ObligationID: lint.ObligationID, Version: lint.Version,
			Seq: tx.NextSeq(), From: domain.ObligationUnresolved, To: domain.ObligationSatisfied, Action: domain.ActionAssertObligation, Actor: user,
			EvidenceIDs: []string{evidence}}, lint.Revision); err != nil {
			return err
		}
		// docs.done: BLOCKED.
		docs := byClaim["docs.done"]
		if _, err := tx.AppendObligationTransition(domain.ObligationTransition{ID: "tr-docs", SessionID: s1, ObligationID: docs.ObligationID, Version: docs.Version,
			Seq: tx.NextSeq(), From: domain.ObligationUnresolved, To: domain.ObligationBlocked, Action: domain.ActionBlockObligation, Actor: user}, docs.Revision); err != nil {
			return err
		}
		return nil
	}))
	// A call in flight (the ledger is not semantic state).
	must(wrap("ledger", s.Update(ctx, s1, func(tx store.Tx) error {
		conv := domain.ConversationIDFor("T", "A")
		c := storetest.NewConversation(s1, conv)
		c.TaskID, c.AgentID = "T", "A"
		if _, err := tx.PutConversation(c, 0); err != nil {
			return err
		}
		call := storetest.NewCall(s1, "call-1", conv, tx.NextSeq())
		call.Principal, call.ServiceActor = principal(s1, domain.AuthorityAgent), harness
		call = storetest.Reseal(call)
		if err := tx.InsertCall(call); err != nil {
			return err
		}
		if err := tx.PutCallAttempt(storetest.NewAttempt(s1, "call-1", 1, tx.NextSeq())); err != nil {
			return err
		}
		call.State, call.Attempts = domain.CallSent, 1
		_, err := tx.UpdateCall(call, 1)
		return err
	})))

	for _, sess := range []string{s1, s2} {
		must(s.View(ctx, sess, func(tx store.ReadTx) error {
			st, err := dump(tx, m.Occurrences[sess], m.Viewers[sess])
			m.Sessions[sess] = st
			return err
		}))
	}
	must(s.Close())
	b, err := json.MarshalIndent(m, "", "  ")
	must(err)
	must(os.WriteFile(filepath.Join(*out, "manifest.json"), append(b, '\n'), 0o644))
}

func dump(tx store.ReadTx, occurrences []string, viewer domain.Principal) (sessionState, error) {
	var st sessionState
	var err error
	st.LastSeq = tx.LastSeq()
	if st.Items, err = tx.Items(store.ItemFilter{}); err != nil {
		return st, err
	}
	if st.Relationships, err = tx.Relationships(store.RelationshipFilter{}); err != nil {
		return st, err
	}
	if st.Obligations, err = tx.Obligations(""); err != nil {
		return st, err
	}
	for _, o := range st.Obligations {
		trs, err := tx.ObligationTransitions(o.ObligationID)
		if err != nil {
			return st, err
		}
		st.Transitions = append(st.Transitions, trs...)
	}
	if ev, err := tx.Event("e-bare"); err == nil {
		st.Events = append(st.Events, ev)
	}
	if st.Grants, err = tx.Grants(); err != nil {
		return st, err
	}
	if st.Lifecycle, err = tx.LifecycleEvents(store.LifecycleFilter{}); err != nil {
		return st, err
	}
	for _, occ := range occurrences {
		r, err := tx.Receipt(occ)
		if err != nil {
			return st, fmt.Errorf("receipt %s: %w", occ, err)
		}
		st.Receipts = append(st.Receipts, r)
		env, err := tx.Envelope(occ)
		if err != nil {
			return st, fmt.Errorf("envelope %s: %w", occ, err)
		}
		st.Envelopes = append(st.Envelopes, env)
		if r.EventID != "" {
			// Phase 2 ingestion keys caller events by receipt; an event
			// record exists only where a store-level writer made one.
			if ev, err := tx.Event(r.EventID); err == nil {
				st.Events = append(st.Events, ev)
			} else if !errors.Is(err, domain.ErrNotFound) {
				return st, fmt.Errorf("event %s: %w", r.EventID, err)
			}
		}
		if t, err := tx.Task(r.Principal.TaskID); err == nil && !containsTask(st.Tasks, t.TaskID) {
			st.Tasks = append(st.Tasks, t)
		}
	}
	if st.Diagnostics, err = tx.Diagnostics(store.DiagnosticFilter{Viewer: viewer}); err != nil {
		return st, err
	}
	if st.Commands, err = tx.LifecycleCommands(store.CommandFilter{Viewer: viewer}); err != nil {
		return st, err
	}
	if st.Calls, err = tx.Calls(store.CallFilter{}); err != nil {
		return st, err
	}
	for _, c := range st.Calls {
		as, err := tx.CallAttempts(c.CallID)
		if err != nil {
			return st, err
		}
		st.Attempts = append(st.Attempts, as...)
		conv, err := tx.Conversation(c.ConversationID)
		if err != nil {
			return st, fmt.Errorf("conversation: %w", err)
		}
		st.Conversations = append(st.Conversations, conv)
	}
	sort.Slice(st.Events, func(i, j int) bool { return st.Events[i].Seq < st.Events[j].Seq })
	return st, nil
}

func wrap(what string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

func containsTask(ts []domain.TaskState, id string) bool {
	for _, t := range ts {
		if t.TaskID == id {
			return true
		}
	}
	return false
}
