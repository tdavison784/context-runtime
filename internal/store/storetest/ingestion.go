package storetest

import (
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// PrivateBoundary is an AGENT-scoped boundary private to agent "agent" in
// task "task": a principal with another agent ID cannot see it.
func PrivateBoundary(sess string) domain.AccessBoundary {
	return domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sess, TaskID: "task", AgentID: "agent"}
}

// NewIngestion returns a valid envelope and receipt for an event by a USER
// principal (NewPrincipal) with two spans: span 0 in the task boundary and
// span 1 private to agent "agent". occurrenceID must match eventID
// (domain.CallerOccurrenceID) or be an anonymous occurrence for an empty
// eventID. The receipt records items (which the caller inserts in the same
// transaction, with EventID eventID), a diagnostic per span, and one
// unresolved Resolve command from span 0.
func NewIngestion(sess, eventID, occurrenceID string, seq uint64, items ...domain.ContextItem) (domain.EventEnvelope, domain.IngestReceipt) {
	p := NewPrincipal(sess, domain.AuthorityUser)
	blob := richBlob(sess)
	event := domain.Event{
		EventID: eventID,
		Kind:    domain.EventUser,
		Spans: []domain.Span{
			{Authority: domain.AuthorityUser, Access: DirectiveBoundary(sess), DirectiveCapable: true,
				Parts: []domain.InputPart{{Type: domain.PartText, MediaType: "text/markdown", Text: "# Resolve goal-1\n\xff"}}},
			{Authority: domain.AuthorityUser, Access: PrivateBoundary(sess),
				Parts:  []domain.InputPart{{Type: domain.PartImage, MediaType: "image/png", Data: blob.Data}},
				Source: &domain.SourceRef{Kind: domain.SourcePath, Locator: "/privé.png"}},
		},
	}
	env, err := domain.NewEventEnvelope(p, occurrenceID, event)
	if err != nil {
		panic("storetest: invalid ingestion fixture: " + err.Error())
	}
	diag := func(span int, access domain.AccessBoundary, code domain.DiagnosticCode, reason domain.DiagnosticReason) domain.DiagnosticRecord {
		return domain.DiagnosticRecord{
			ID: domain.DiagnosticRecordID(sess, occurrenceID, span, 0), SessionID: sess, OccurrenceID: occurrenceID, EventID: eventID,
			Access: access, SchemaVersion: domain.DiagnosticSchemaVersion,
			Diagnostic: domain.Diagnostic{SpanIndex: span, Code: code, Reason: reason, Range: domain.ByteRange{Start: 0, End: 16}, ParserVersion: "parser/v1"},
		}
	}
	actor, _ := domain.SourceActor(p, domain.AuthorityUser)
	cmd := domain.LifecycleCommandRecord{
		ID: domain.LifecycleCommandRecordID(sess, occurrenceID, 0), SessionID: sess, OccurrenceID: occurrenceID, EventID: eventID,
		Actor: actor, Access: DirectiveBoundary(sess), ParserVersion: "parser/v1", SchemaVersion: domain.LifecycleCommandSchemaVersion,
		Status: domain.CommandParsedNotExecuted, Resolution: domain.TargetNotFound,
		LifecycleCommand: domain.LifecycleCommand{Action: domain.LifecycleResolve, TargetID: "goal-1", Authority: domain.AuthorityUser, Range: domain.ByteRange{Start: 0, End: 16}},
	}
	r := domain.IngestReceipt{
		SessionID: sess, OccurrenceID: occurrenceID, EventID: eventID, Principal: p, PayloadHash: env.PayloadHash, Seq: seq,
		Items: items,
		Diagnostics: []domain.DiagnosticRecord{
			diag(0, DirectiveBoundary(sess), domain.DiagnosticNotFound, domain.ReasonUnknownTarget),
			diag(1, PrivateBoundary(sess), domain.DirectiveNotParsed, domain.ReasonSourceNotCapable),
		},
		Lifecycle:     []domain.LifecycleCommandRecord{cmd},
		Versions:      domain.ExecutionVersions{Parser: "parser/v1", Policy: "policy/v1", Limits: domain.DefaultLimits()},
		SchemaVersion: domain.IngestReceiptSchemaVersion,
	}
	return env, r
}

// ingestedItem returns a transcript item created by event eventID.
func ingestedItem(sess, id, eventID string, seq uint64) domain.ContextItem {
	it := NewTranscript(sess, id, seq, "# Resolve goal-1\n\xff")
	it.EventID = eventID
	return it
}

// ingest inserts the items and the ingestion in one Update and returns the
// stored envelope and receipt.
func ingest(t *testing.T, s store.Store, sess, eventID, occurrenceID string) (domain.EventEnvelope, domain.IngestReceipt) {
	t.Helper()
	var env domain.EventEnvelope
	var r domain.IngestReceipt
	update(t, s, sess, func(tx store.Tx) error {
		noErr(t, tx.InsertBlob(richBlob(sess)))
		it := ingestedItem(sess, domain.DerivedItemID(sess, occurrenceID, 0), eventID, tx.NextSeq())
		noErr(t, tx.InsertItem(it))
		env, r = NewIngestion(sess, eventID, occurrenceID, tx.NextSeq(), it)
		return tx.InsertIngestion(env, r)
	})
	return env, r
}

var anonymousIDs = &domain.SequentialIDs{}

func testIngestionRoundTrip(t *testing.T, s store.Store) {
	occ := domain.CallerOccurrenceID(sessA, "evt-1")
	var env domain.EventEnvelope
	var r domain.IngestReceipt
	update(t, s, sessA, func(tx store.Tx) error {
		// The idempotency lookup needs no sequence number.
		_, err := tx.Receipt(occ)
		wantErr(t, err, domain.ErrNotFound)
		if tx.LastSeq() != 0 {
			t.Errorf("receipt lookup changed LastSeq to %d", tx.LastSeq())
		}
		noErr(t, tx.InsertBlob(richBlob(sessA)))
		it := ingestedItem(sessA, "itm-0", "evt-1", tx.NextSeq())
		noErr(t, tx.InsertItem(it))
		env, r = NewIngestion(sessA, "evt-1", occ, tx.NextSeq(), it)
		noErr(t, tx.InsertIngestion(env, r))
		got, err := tx.Receipt(occ)
		noErr(t, err)
		assertEqual(t, "receipt inside Update", got, r)
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Receipt(occ)
		noErr(t, err)
		assertEqual(t, "receipt", got, r)
		gotEnv, err := tx.Envelope(occ)
		noErr(t, err)
		assertEqual(t, "envelope", gotEnv, env)
		noErr(t, gotEnv.Validate())
		if part := gotEnv.Event.Spans[1].Parts[0]; part.Data != nil || part.BlobHash != richBlob(sessA).Hash {
			t.Errorf("envelope image part = %+v, want a blob reference without bytes", part)
		}
		_, err = tx.Receipt(domain.CallerOccurrenceID(sessA, "evt-2"))
		wantErr(t, err, domain.ErrNotFound)
		_, err = tx.Envelope("missing")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
	view(t, s, sessB, func(tx store.ReadTx) error {
		_, err := tx.Receipt(occ)
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

// testReceiptKeepsOriginalItems checks that a receipt returns the item
// values as created, not the item's later state (D14).
func testReceiptKeepsOriginalItems(t *testing.T, s store.Store) {
	occ := domain.CallerOccurrenceID(sessA, "evt-1")
	_, r := ingest(t, s, sessA, "evt-1", occ)
	id := r.Items[0].ID
	update(t, s, sessA, func(tx store.Tx) error {
		archived := domain.ResidencyArchived
		_, err := tx.UpdateItem(id, 1, domain.ItemChange{Residency: &archived}, NewItemEvent(sessA, "archive", tx.NextSeq(), id))
		return err
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		cur, err := tx.Item(id)
		noErr(t, err)
		if cur.Version != 2 {
			t.Fatalf("item version = %d, want 2", cur.Version)
		}
		got, err := tx.Receipt(occ)
		noErr(t, err)
		assertEqual(t, "receipt after a lifecycle change", got, r)
		return nil
	})
}

func testIngestionInsertRules(t *testing.T, s store.Store) {
	occ := domain.CallerOccurrenceID(sessA, "evt-1")
	ingest(t, s, sessA, "evt-1", occ)
	// An item of event evt-2 stored by an earlier transaction.
	var early domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		early = ingestedItem(sessA, "early", "evt-2", tx.NextSeq())
		return tx.InsertItem(early)
	})
	type fixture struct {
		env domain.EventEnvelope
		r   domain.IngestReceipt
	}
	// fresh stores a new item and returns a valid ingestion of event
	// "evt-2" recording it.
	fresh := func(tx store.Tx) fixture {
		o := domain.CallerOccurrenceID(sessA, "evt-2")
		it := ingestedItem(sessA, "itm-evt-2", "evt-2", tx.NextSeq())
		noErr(t, tx.InsertItem(it))
		env, r := NewIngestion(sessA, "evt-2", o, tx.NextSeq(), it)
		return fixture{env, r}
	}
	cases := []struct {
		name   string
		mutate func(tx store.Tx, f *fixture)
		want   error
	}{
		{"valid", func(store.Tx, *fixture) {}, nil},
		{"receipt seq not allocated", func(_ store.Tx, f *fixture) { f.r.Seq = 1 }, domain.ErrInvalidRecord},
		{"invalid receipt", func(_ store.Tx, f *fixture) { f.r.SchemaVersion = "" }, domain.ErrInvalidRecord},
		{"payload hash disagrees", func(_ store.Tx, f *fixture) {
			f.r.PayloadHash = domain.HashBytes(nil)
		}, domain.ErrInvalidRecord},
		{"principal disagrees", func(_ store.Tx, f *fixture) {
			f.r.Principal.AgentID = "other"
			for i := range f.r.Lifecycle {
				f.r.Lifecycle[i].Actor.AgentID = "other"
				f.r.Lifecycle[i].Access = DirectiveBoundary(sessA)
			}
		}, domain.ErrInvalidRecord},
		{"item not stored", func(tx store.Tx, f *fixture) {
			f.r.Items = append(f.r.Items, ingestedItem(sessA, "ghost", "evt-2", tx.NextSeq()))
		}, domain.ErrInvalidRecord},
		{"item snapshot differs", func(_ store.Tx, f *fixture) {
			f.r.Items[0].Residency = domain.ResidencyArchived
		}, domain.ErrInvalidRecord},
		{"item from an earlier transaction", func(_ store.Tx, f *fixture) {
			f.r.Items = []domain.ContextItem{early}
		}, domain.ErrInvalidRecord},
		{"link to a missing item", func(_ store.Tx, f *fixture) {
			f.r.Duplicates = []domain.IngestLink{{ItemID: f.r.Items[0].ID, TargetID: "missing"}}
		}, domain.ErrInvalidRecord},
		{"resolved command target missing", func(_ store.Tx, f *fixture) {
			c := &f.r.Lifecycle[0]
			c.Resolution, c.ResolvedItemID, c.ResolvedVersion = domain.TargetResolved, "missing", 1
		}, domain.ErrInvalidRecord},
		{"envelope references a missing blob", func(_ store.Tx, f *fixture) {
			f.env.Event.Spans[1].Parts[0].BlobHash = domain.HashBytes([]byte("absent"))
			f.env.PayloadHash, _ = f.env.Event.PayloadHash(f.env.Principal)
			f.r.PayloadHash = f.env.PayloadHash
		}, domain.ErrIntegrity},
		{"envelope blob size differs", func(_ store.Tx, f *fixture) {
			f.env.Event.Spans[1].Parts[0].BlobSize++
			f.env.PayloadHash, _ = f.env.Event.PayloadHash(f.env.Principal)
			f.r.PayloadHash = f.env.PayloadHash
		}, domain.ErrIntegrity},
		{"foreign session", func(_ store.Tx, f *fixture) {
			f.env, f.r = NewIngestion(sessB, "evt-2", domain.CallerOccurrenceID(sessB, "evt-2"), f.r.Seq)
		}, domain.ErrInvalidRecord},
		{"occurrence reused", func(_ store.Tx, f *fixture) {
			f.env, f.r = NewIngestion(sessA, "evt-1", occ, f.r.Seq)
		}, domain.ErrImmutable},
		{"occurrence reused with another payload", func(_ store.Tx, f *fixture) {
			f.env, f.r = NewIngestion(sessA, "evt-1", occ, f.r.Seq)
			f.env.Event.Spans[0].Parts[0].Text = "different"
			f.env.PayloadHash, _ = f.env.Event.PayloadHash(f.env.Principal)
			f.r.PayloadHash = f.env.PayloadHash
		}, domain.ErrEventIDConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.Update(ctx, sessA, func(tx store.Tx) error {
				f := fresh(tx)
				c.mutate(tx, &f)
				err := tx.InsertIngestion(f.env, f.r)
				if c.want == nil {
					noErr(t, err)
				} else {
					wantErr(t, err, c.want)
				}
				if c.want != nil {
					_, err = tx.Receipt(f.r.OccurrenceID)
					if f.r.OccurrenceID != occ {
						wantErr(t, err, domain.ErrNotFound)
					}
				}
				return errRollback
			})
			// A rejected ingestion follows the fixture's item writes, so it
			// poisons the transaction and Update returns the rejection (P3-1).
			if c.want != nil {
				wantErr(t, err, c.want)
			} else {
				wantErr(t, err, errRollback)
			}
		})
	}
	// Rolled back: nothing of evt-2 remains.
	view(t, s, sessA, func(tx store.ReadTx) error {
		o := domain.CallerOccurrenceID(sessA, "evt-2")
		_, err := tx.Receipt(o)
		wantErr(t, err, domain.ErrNotFound)
		_, err = tx.Envelope(o)
		wantErr(t, err, domain.ErrNotFound)
		ds, err := tx.Diagnostics(store.DiagnosticFilter{Viewer: NewPrincipal(sessA, domain.AuthorityUser), OccurrenceID: o})
		noErr(t, err)
		if len(ds) != 0 {
			t.Errorf("rolled-back diagnostics = %d", len(ds))
		}
		return nil
	})
}

// testAnonymousIngestions checks that events without an EventID never
// alias: equal diagnostic keys under distinct occurrences are distinct
// records (D16, M3).
func testAnonymousIngestions(t *testing.T, s store.Store) {
	occ1 := domain.NewAnonymousOccurrenceID(anonymousIDs)
	occ2 := domain.NewAnonymousOccurrenceID(anonymousIDs)
	_, r1 := ingest(t, s, sessA, "", occ1)
	_, r2 := ingest(t, s, sessA, "", occ2)
	view(t, s, sessA, func(tx store.ReadTx) error {
		for _, want := range []domain.IngestReceipt{r1, r2} {
			got, err := tx.Receipt(want.OccurrenceID)
			noErr(t, err)
			assertEqual(t, "anonymous receipt", got, want)
		}
		ds, err := tx.Diagnostics(store.DiagnosticFilter{Viewer: NewPrincipal(sessA, domain.AuthorityUser)})
		noErr(t, err)
		ids := map[string]bool{}
		for _, d := range ds {
			ids[d.ID] = true
		}
		if len(ds) != 4 || len(ids) != 4 {
			t.Errorf("diagnostics = %d records with %d IDs, want 4 distinct", len(ds), len(ids))
		}
		return nil
	})
}

// testDiagnosticsAccess checks that diagnostic and command reads return only
// records whose source span boundary permits the viewer, in stable order
// (D16, FR-DIR-005).
func testDiagnosticsAccess(t *testing.T, s store.Store) {
	_, r1 := ingest(t, s, sessA, "evt-1", domain.CallerOccurrenceID(sessA, "evt-1"))
	_, r2 := ingest(t, s, sessA, "evt-2", domain.CallerOccurrenceID(sessA, "evt-2"))
	owner := NewPrincipal(sessA, domain.AuthorityUser)
	otherAgent := owner
	otherAgent.AgentID = "other-agent"
	otherTask := owner
	otherTask.TaskID = "task2"
	view(t, s, sessA, func(tx store.ReadTx) error {
		all, err := tx.Diagnostics(store.DiagnosticFilter{Viewer: owner})
		noErr(t, err)
		want := append(append([]domain.DiagnosticRecord{}, r1.Diagnostics...), r2.Diagnostics...)
		assertEqual(t, "owner diagnostics", all, want)

		one, err := tx.Diagnostics(store.DiagnosticFilter{Viewer: owner, OccurrenceID: r2.OccurrenceID})
		noErr(t, err)
		assertEqual(t, "one event", one, r2.Diagnostics)

		// The private span's diagnostic is invisible to another agent.
		got, err := tx.Diagnostics(store.DiagnosticFilter{Viewer: otherAgent})
		noErr(t, err)
		assertEqual(t, "other agent", got, []domain.DiagnosticRecord{r1.Diagnostics[0], r2.Diagnostics[0]})
		got, err = tx.Diagnostics(store.DiagnosticFilter{Viewer: otherTask})
		noErr(t, err)
		assertEqual(t, "other task", got, []domain.DiagnosticRecord{})

		cmds, err := tx.LifecycleCommands(store.CommandFilter{Viewer: owner})
		noErr(t, err)
		assertEqual(t, "commands", cmds, append(append([]domain.LifecycleCommandRecord{}, r1.Lifecycle...), r2.Lifecycle...))
		cmds, err = tx.LifecycleCommands(store.CommandFilter{Viewer: otherTask})
		noErr(t, err)
		assertEqual(t, "commands for another task", cmds, []domain.LifecycleCommandRecord{})

		_, err = tx.Diagnostics(store.DiagnosticFilter{})
		wantErr(t, err, domain.ErrInvalidRecord)
		_, err = tx.LifecycleCommands(store.CommandFilter{})
		wantErr(t, err, domain.ErrInvalidRecord)
		return nil
	})
	view(t, s, sessB, func(tx store.ReadTx) error {
		got, err := tx.Diagnostics(store.DiagnosticFilter{Viewer: NewPrincipal(sessB, domain.AuthorityUser)})
		noErr(t, err)
		assertEqual(t, "another session", got, []domain.DiagnosticRecord{})
		return nil
	})
}

// testIngestionDeepCopies checks that receipts and envelopes are copied on
// the way in and out.
func testIngestionDeepCopies(t *testing.T, s store.Store) {
	occ := domain.CallerOccurrenceID(sessA, "evt-1")
	var want domain.IngestReceipt
	var wantEnv domain.EventEnvelope
	scribble := func(env *domain.EventEnvelope, r *domain.IngestReceipt) {
		r.Items[0].Parts[0].Text = "scribbled"
		r.Items[0].Tags[0] = "scribbled"
		r.Diagnostics[0].Reason = domain.ReasonLimit
		r.Lifecycle[0].TargetID = "scribbled"
		env.Event.Spans[0].Parts[0].Text = "scribbled"
		env.Event.Spans[1].Source.Locator = "scribbled"
	}
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertBlob(richBlob(sessA)))
		it := ingestedItem(sessA, "itm-0", "evt-1", tx.NextSeq())
		noErr(t, tx.InsertItem(it))
		env, r := NewIngestion(sessA, "evt-1", occ, tx.NextSeq(), it)
		noErr(t, tx.InsertIngestion(env, r))
		want, wantEnv = r.Clone(), env.Clone()
		scribble(&env, &r)
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r, err := tx.Receipt(occ)
		noErr(t, err)
		env, err := tx.Envelope(occ)
		noErr(t, err)
		assertEqual(t, "receipt after mutating the input", r, want)
		assertEqual(t, "envelope after mutating the input", env, wantEnv)
		scribble(&env, &r)
		ds, err := tx.Diagnostics(store.DiagnosticFilter{Viewer: NewPrincipal(sessA, domain.AuthorityUser)})
		noErr(t, err)
		ds[0].Code = domain.DiagnosticsTruncated
		r, err = tx.Receipt(occ)
		noErr(t, err)
		env, err = tx.Envelope(occ)
		noErr(t, err)
		assertEqual(t, "receipt after mutating a read", r, want)
		assertEqual(t, "envelope after mutating a read", env, wantEnv)
		return nil
	})
}

func testIngestionAcrossRestart(t *testing.T, open Opener) {
	s := openDurable(t, open)
	occ := domain.CallerOccurrenceID(sessA, "evt-1")
	env, r := ingest(t, s, sessA, "evt-1", occ)
	anon := domain.NewAnonymousOccurrenceID(anonymousIDs)
	_, ra := ingest(t, s, sessA, "", anon)
	s = reopen(t, s, open)
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Receipt(occ)
		noErr(t, err)
		assertEqual(t, "receipt after restart", got, r)
		gotEnv, err := tx.Envelope(occ)
		noErr(t, err)
		assertEqual(t, "envelope after restart", gotEnv, env)
		noErr(t, gotEnv.Validate())
		got, err = tx.Receipt(anon)
		noErr(t, err)
		assertEqual(t, "anonymous receipt after restart", got, ra)
		ds, err := tx.Diagnostics(store.DiagnosticFilter{Viewer: NewPrincipal(sessA, domain.AuthorityUser)})
		noErr(t, err)
		assertEqual(t, "diagnostics after restart", ds, append(append([]domain.DiagnosticRecord{}, r.Diagnostics...), ra.Diagnostics...))
		return nil
	})
}

// testReceiptRecordsEveryLimit checks that a receipt records every
// execution limit exactly (D14): each domain.Limits field, found by
// reflection so a new limit cannot be forgotten, gets a distinct
// non-default value that must round-trip.
func testReceiptRecordsEveryLimit(t *testing.T, s store.Store) {
	occ := domain.CallerOccurrenceID(sessA, "evt-limits")
	var want domain.IngestReceipt
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertBlob(richBlob(sessA)))
		env, r := NewIngestion(sessA, "evt-limits", occ, tx.NextSeq())
		v := reflect.ValueOf(&r.Versions.Limits).Elem()
		for i := range v.NumField() {
			v.Field(i).SetInt(int64(i + 2)) // distinct, positive, within every grammar bound
		}
		noErr(t, r.Versions.Validate())
		want = r
		return tx.InsertIngestion(env, r)
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Receipt(occ)
		noErr(t, err)
		assertEqual(t, "receipt limits", got.Versions, want.Versions)
		return nil
	})
}
