package storetest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// testDeepCopies checks that stores copy records on the way in and out:
// mutating a record after inserting it, or mutating any slice or pointer in
// a returned record, never changes stored state, inside or after the
// transaction.
func testDeepCopies(t *testing.T, s store.Store) {
	type fixture struct {
		item  domain.ContextItem
		rel   domain.Relationship
		event domain.EventRecord
		blob  domain.Blob
		obl   domain.ObligationVersion
		tr    domain.ObligationTransition
		grant domain.MutationGrant
		call  domain.CallRecord
	}
	// fresh returns the records as stored after the first transaction; the
	// obligation reflects transition t.
	fresh := func() fixture {
		// The call is completed through one attempt sent at seq 4.
		call := NewCall(sessA, "call", "conv", 1)
		call.Attempts, call.Revision = 1, 3
		call = Finish(call, domain.CallCompleted, 5)
		in := int64(10)
		call.Outcome.Usage = []domain.UsageIteration{{Iteration: 1, InputTokens: &in}}
		call.OutcomeHash = call.Outcome.OutcomeHash()
		obl := NewObligation(sessA, "o", 1, 1, "rich")
		// BLOCKED: the raw Phase 2 path never satisfies (INV-16, DUR-2.12).
		obl.Status, obl.Revision = domain.ObligationBlocked, 2
		obl.Claim = "tests_pass"
		rel := NewRelationship(sessA, "r", domain.RelDerivedFrom, "rich", "plain", 2)
		rel.Coverage = &domain.Coverage{ConversationID: "conv", FromSeq: 1, ToSeq: 2}
		return fixture{
			item:  richItem(sessA, "rich", 1),
			rel:   rel,
			event: NewEvent(sessA, "e", 1, "p"),
			blob:  NewBlob(sessA, []byte("bytes")),
			obl:   obl,
			tr:    NewTransition(sessA, "t", "o", 1, 2, domain.ObligationUnresolved, domain.ObligationBlocked),
			grant: NewGrant(sessA, "g", 1, "rich"),
			call:  call,
		}
	}
	scribble := func(f *fixture) {
		f.item.Parts[0].Text = "scribbled"
		f.item.Tags[0] = "scribbled"
		*f.item.GoalStatus = domain.GoalResolved
		*f.item.TTLTurns = 99
		f.item.Source.Locator = "scribbled"
		f.item.SourceRanges[0].Slices[0].Start = 99
		f.item.SourceRanges[1].TranscriptID = "scribbled"
		f.rel.Coverage.ToSeq = 99
		f.event.ItemIDs[0] = "scribbled"
		f.blob.Data[0] = 'X'
		f.obl.Matcher.Name = "scribbled"
		f.tr.EvidenceIDs[0] = "scribbled"
		f.tr.Fingerprints[0] = "scribbled"
		f.tr.Matcher = nil
		f.grant.TargetIDs[0] = "scribbled"
		f.grant.Grantee.AgentID = "scribbled"
		f.call.Request[0] = 'X'
		f.call.Outcome.Response[0] = 'X'
		*f.call.Outcome.Usage[0].InputTokens = 99
	}
	read := func(t *testing.T, tx store.ReadTx) fixture {
		t.Helper()
		var f fixture
		var err error
		f.item, err = tx.Item("rich")
		noErr(t, err)
		rels, err := tx.Relationships(store.RelationshipFilter{})
		noErr(t, err)
		if len(rels) != 1 {
			t.Fatalf("Relationships = %d, want 1", len(rels))
		}
		f.rel = rels[0]
		f.event, err = tx.Event("e")
		noErr(t, err)
		f.blob, err = tx.Blob(fresh().blob.Hash)
		noErr(t, err)
		f.obl, err = tx.Obligation("o")
		noErr(t, err)
		trs, err := tx.ObligationTransitions("o")
		noErr(t, err)
		if len(trs) != 1 {
			t.Fatalf("ObligationTransitions = %d, want 1", len(trs))
		}
		f.tr = trs[0]
		f.grant, err = tx.Grant("g")
		noErr(t, err)
		f.call, err = tx.Call("call")
		noErr(t, err)
		return f
	}
	check := func(t *testing.T, tx store.ReadTx, when string) {
		t.Helper()
		got, want := read(t, tx), fresh()
		assertEqual(t, when+": item", got.item, want.item)
		assertEqual(t, when+": relationship", got.rel, want.rel)
		assertEqual(t, when+": event", got.event, want.event)
		assertEqual(t, when+": blob", got.blob, want.blob)
		assertEqual(t, when+": obligation", got.obl, want.obl)
		assertEqual(t, when+": transition", got.tr, want.tr)
		assertEqual(t, when+": grant", got.grant, want.grant)
		assertEqual(t, when+": call", got.call, want.call)
	}

	update(t, s, sessA, func(tx store.Tx) error {
		seqs(tx, 5)
		f := fresh()
		noErr(t, tx.InsertBlob(richBlob(sessA)))
		noErr(t, tx.InsertItem(f.item))
		noErr(t, tx.InsertItem(NewItem(sessA, "plain", 2, "plain")))
		noErr(t, tx.InsertRelationship(f.rel))
		_, _, err := tx.InsertEvent(f.event)
		noErr(t, err)
		noErr(t, tx.InsertBlob(f.blob))
		obl := NewObligation(sessA, "o", 1, 1, "rich")
		obl.Claim = "tests_pass"
		noErr(t, tx.InsertObligationVersion(obl))
		obl.Matcher.Name = "scribbled"
		applied, err := tx.AppendObligationTransition(f.tr, 1)
		noErr(t, err)
		applied.Matcher.Name = "scribbled"
		noErr(t, tx.InsertGrant(f.grant))
		noErr(t, tx.InsertCall(NewCall(sessA, "call", "conv", 1)))
		noErr(t, tx.PutCallAttempt(NewAttempt(sessA, "call", 1, 4)))
		sent := NewCall(sessA, "call", "conv", 1)
		sent.State, sent.Attempts = domain.CallSent, 1
		_, err = tx.UpdateCall(sent, 1)
		noErr(t, err)
		noErr(t, tx.PutCallAttempt(CloseAttempt(NewAttempt(sessA, "call", 1, 4), domain.AttemptCompleted, f.call.OutcomeHash, 5)))
		_, err = tx.UpdateCall(f.call, 2)
		noErr(t, err)

		scribble(&f)
		check(t, tx, "after mutating inserted records")
		out := read(t, tx)
		scribble(&out)
		check(t, tx, "after mutating records read inside Update")

		// Records returned by writes are copies too.
		stored, existed, err := tx.InsertEvent(fresh().event)
		noErr(t, err)
		if !existed {
			t.Errorf("repeated InsertEvent existed = false")
		}
		stored.ItemIDs[0] = "scribbled"
		it, err := tx.UpdateItem("rich", 1, domain.ItemChange{}, NewItemEvent(sessA, "l", 3, "rich"))
		noErr(t, err)
		it.Tags[0] = "scribbled"
		it.Parts[0].Text = "scribbled"
		ov, err := tx.UpdateObligationVersion(fresh().obl, 2)
		noErr(t, err)
		ov.Matcher.Name = "scribbled"
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		out := read(t, tx)
		if out.item.Version != 2 || out.obl.Revision != 3 {
			t.Fatalf("versions = (item %d, obligation revision %d), want (2, 3)", out.item.Version, out.obl.Revision)
		}
		want := fresh()
		want.item.Version, want.obl.Revision = 2, 3
		scribble(&out)
		got := read(t, tx)
		assertEqual(t, "item after mutating a committed read", got.item, want.item)
		assertEqual(t, "event after mutating a committed read", got.event, want.event)
		assertEqual(t, "obligation after mutating a committed read", got.obl, want.obl)
		assertEqual(t, "call after mutating a committed read", got.call, want.call)
		assertEqual(t, "grant after mutating a committed read", got.grant, want.grant)
		assertEqual(t, "blob after mutating a committed read", got.blob, want.blob)

		// List results are copies as well.
		items, err := tx.Items(store.ItemFilter{})
		noErr(t, err)
		for i := range items {
			items[i].Tags = append(items[i].Tags[:0], "scribbled")
		}
		grants, err := tx.Grants()
		noErr(t, err)
		grants[0].TargetIDs[0] = "scribbled"
		calls, err := tx.Calls(store.CallFilter{})
		noErr(t, err)
		calls[0].Request[0] = 'X'
		vers, err := tx.ObligationVersions("o")
		noErr(t, err)
		vers[0].Matcher.Name = "scribbled"
		obls, err := tx.Obligations("")
		noErr(t, err)
		obls[0].Matcher.Version = "scribbled"
		got = read(t, tx)
		assertEqual(t, "item after mutating a list result", got.item, want.item)
		assertEqual(t, "grant after mutating a list result", got.grant, want.grant)
		assertEqual(t, "call after mutating a list result", got.call, want.call)
		assertEqual(t, "obligation after mutating a list result", got.obl, want.obl)
		return nil
	})
}
