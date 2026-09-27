package storetest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// SemanticDirective is NewDirective with the explicit DIRECTIVE namespace
// Phase 3 writes require (P3-3).
func SemanticDirective(sess, id, dirID string, seq uint64, text string) domain.ContextItem {
	it := NewDirective(sess, id, dirID, seq, text)
	it.Namespace = domain.NamespaceDirective
	return it
}

// CreationDeclarationFor returns the known creation declaration of stored
// item it, built from the item's own creation fields the way the graph
// builds it, with the given accepted attributes and support set.
func CreationDeclarationFor(t *testing.T, it domain.ContextItem, id string, seq uint64, attributes, support []string) domain.CreationDeclaration {
	t.Helper()
	key, ok := it.CurrentKey()
	if !ok {
		t.Fatalf("item %s has no current key", it.ID)
	}
	sem := domain.CreationSemantics{Key: key, Authority: it.Authority, WorkflowID: it.WorkflowID, AgentID: it.AgentID, Section: it.Section, Kind: it.Kind,
		ContentHash: it.ContentHash, AcceptedAttributes: attributes, SupportIDs: support, Generation: it.Generation, Retention: it.Retention,
		Residency: it.Residency, GoalStatus: it.GoalStatus, OriginTaskID: it.TaskID, OriginTurnID: it.TurnID, CreatedTurn: it.CreatedTurn, TTLTurns: it.TTLTurns}
	sig, err := sem.Signature(domain.Phase3PolicyVersion)
	if err != nil {
		t.Fatal(err)
	}
	return domain.CreationDeclaration{SemanticMeta: Meta(it.SessionID, id, seq), ItemID: it.ID, PolicyVersion: domain.Phase3PolicyVersion,
		Signature: sig, LegacyKnown: true, AcceptedSemantics: sem}
}

// testSemanticCreationDeclarations checks immutable creation declarations
// (P3-4): one per stored item, semantics equal to the item's own creation
// fields, existing support sources, and explicit unknown identity.
func testSemanticCreationDeclarations(t *testing.T, s store.Store) {
	var it, src domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		it, src = SemanticDirective(sessA, "p1", "dep", tx.NextSeq(), "run tests"), NewItem(sessA, "src", tx.NextSeq(), "evidence")
		noErr(t, tx.InsertItem(it))
		noErr(t, tx.InsertItem(src))
		return semantic(t, tx).InsertCreationDeclaration(CreationDeclarationFor(t, it, "decl-p1", tx.NextSeq(), []string{"kind=constraint"}, []string{"src"}))
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := readSemantic(t, tx).CreationDeclaration("p1")
		noErr(t, err)
		assertEqual(t, "CreationDeclaration", got, CreationDeclarationFor(t, it, "decl-p1", 3, []string{"kind=constraint"}, []string{"src"}))
		// Legacy absence is unknown identity, reported as ErrNotFound.
		_, err = readSemantic(t, tx).CreationDeclaration("src")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
	for _, tc := range []struct {
		name string
		d    func(tx store.Tx) domain.CreationDeclaration
		want error
	}{
		{"second declaration of an item", func(tx store.Tx) domain.CreationDeclaration {
			return CreationDeclarationFor(t, it, "decl-p1b", tx.NextSeq(), nil, nil)
		}, domain.ErrImmutable},
		{"missing item", func(tx store.Tx) domain.CreationDeclaration {
			ghost := it
			ghost.ID = "ghost"
			return CreationDeclarationFor(t, ghost, "decl-ghost", tx.NextSeq(), nil, nil)
		}, domain.ErrInvalidRecord},
		{"semantics disagree with the item", func(tx store.Tx) domain.CreationDeclaration {
			other := it
			other.ID = "src"
			return CreationDeclarationFor(t, other, "decl-src", tx.NextSeq(), nil, nil)
		}, domain.ErrInvalidRecord},
		{"missing support source", func(tx store.Tx) domain.CreationDeclaration {
			d := CreationDeclarationFor(t, it, "decl-x", tx.NextSeq(), nil, []string{"ghost"})
			d.ItemID = "p2"
			return d
		}, domain.ErrInvalidRecord},
		{"unallocated sequence", func(store.Tx) domain.CreationDeclaration {
			d := CreationDeclarationFor(t, it, "decl-x", 1, nil, nil)
			d.ItemID = "p2"
			return d
		}, domain.ErrInvalidRecord},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			if tc.name == "missing support source" {
				p2 := SemanticDirective(sessA, "p2", "dep2", tx.NextSeq(), "p2")
				noErr(t, tx.InsertItem(p2))
				d := CreationDeclarationFor(t, p2, "decl-x", tx.NextSeq(), nil, []string{"ghost"})
				return semantic(t, tx).InsertCreationDeclaration(d)
			}
			return semantic(t, tx).InsertCreationDeclaration(tc.d(tx))
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	// An explicit unknown declaration records that identity is unknown.
	update(t, s, sessA, func(tx store.Tx) error {
		return semantic(t, tx).InsertCreationDeclaration(domain.CreationDeclaration{SemanticMeta: Meta(sessA, "decl-src", tx.NextSeq()), ItemID: "src", PolicyVersion: domain.Phase3PolicyVersion})
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := readSemantic(t, tx).CreationDeclaration("src")
		noErr(t, err)
		if got.LegacyKnown || got.Signature != "" {
			t.Errorf("unknown declaration = %+v", got)
		}
		return nil
	})
}

// testSemanticSnapshotDeclarations checks ordered Working snapshot
// declarations (P3-4): members name stored declarations of their items with
// the same signature, in one authority/task/boundary partition.
func testSemanticSnapshotDeclarations(t *testing.T, s store.Store) {
	var a, b domain.CreationDeclaration
	var itA domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		itA = SemanticDirective(sessA, "a", "a", tx.NextSeq(), "a")
		itB := SemanticDirective(sessA, "b", "b", tx.NextSeq(), "b")
		noErr(t, tx.InsertItem(itA))
		noErr(t, tx.InsertItem(itB))
		a, b = CreationDeclarationFor(t, itA, "da", tx.NextSeq(), nil, nil), CreationDeclarationFor(t, itB, "db", tx.NextSeq(), nil, nil)
		noErr(t, semantic(t, tx).InsertCreationDeclaration(a))
		return semantic(t, tx).InsertCreationDeclaration(b)
	})
	snap := func(seq uint64, members ...domain.SnapshotDeclarationMember) domain.SnapshotDeclaration {
		sd := domain.SnapshotDeclaration{SemanticMeta: Meta(sessA, "snap", seq), TaskID: "task", Authority: itA.Authority, Access: itA.Access,
			PolicyVersion: domain.Phase3PolicyVersion, Members: members, LegacyKnown: true}
		sig, err := sd.CanonicalSignature()
		noErr(t, err)
		sd.Signature = sig
		return sd
	}
	member := func(d domain.CreationDeclaration) domain.SnapshotDeclarationMember {
		return domain.SnapshotDeclarationMember{ItemID: d.ItemID, DeclarationID: d.ID, Signature: d.Signature}
	}
	for _, tc := range []struct {
		name string
		m    domain.SnapshotDeclarationMember
	}{
		{"missing declaration", domain.SnapshotDeclarationMember{ItemID: "a", DeclarationID: "nope", Signature: a.Signature}},
		{"declaration of another item", domain.SnapshotDeclarationMember{ItemID: "b", DeclarationID: "da", Signature: a.Signature}},
		{"signature disagrees", domain.SnapshotDeclarationMember{ItemID: "a", DeclarationID: "da", Signature: b.Signature}},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			return semantic(t, tx).InsertSnapshotDeclaration(snap(tx.NextSeq(), tc.m))
		})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("%s: error = %v, want ErrInvalidRecord", tc.name, err)
		}
	}
	// Members must share the snapshot's partition.
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
		sd := snap(tx.NextSeq(), member(a))
		sd.Authority = domain.AuthoritySystem
		sig, err := sd.CanonicalSignature()
		noErr(t, err)
		sd.Signature = sig
		return semantic(t, tx).InsertSnapshotDeclaration(sd)
	})
	var want domain.SnapshotDeclaration
	update(t, s, sessA, func(tx store.Tx) error {
		want = snap(tx.NextSeq(), member(b), member(a))
		return semantic(t, tx).InsertSnapshotDeclaration(want)
	})
	rejected(t, s, sessA, domain.ErrImmutable, func(tx store.Tx) error {
		return semantic(t, tx).InsertSnapshotDeclaration(snap(tx.NextSeq(), member(a)))
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := readSemantic(t, tx).SnapshotDeclaration("snap")
		noErr(t, err)
		assertEqual(t, "SnapshotDeclaration", got, want)
		return nil
	})
}

// testSemanticCurrentPointerCAS checks the Phase 3 current-version write
// (P3-3): an empty expected prior files a key for the first time only, a
// stale expectation conflicts, and duplicates, superseded items, and items
// without an explicit namespace never become current.
func testSemanticCurrentPointerCAS(t *testing.T, s store.Store) {
	key := func(tx store.ReadTx) string {
		id, err := tx.CurrentVersion(domain.CurrentKey{SessionID: sessA, TaskID: "task", Access: DirectiveBoundary(sessA), Namespace: domain.NamespaceDirective, ID: "dep"})
		if errors.Is(err, domain.ErrNotFound) {
			return ""
		}
		noErr(t, err)
		return id
	}
	update(t, s, sessA, func(tx store.Tx) error {
		for _, id := range []string{"p1", "p2", "p3"} {
			noErr(t, tx.InsertItem(SemanticDirective(sessA, id, "dep", tx.NextSeq(), id)))
		}
		noErr(t, tx.InsertItem(NewDirective(sessA, "legacy", "dep", tx.NextSeq(), "legacy")))
		noErr(t, tx.InsertItem(SemanticDirective(sessA, "dup", "dep", tx.NextSeq(), "p1")))
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "dup-of", domain.RelDuplicateOf, "dup", "p1", tx.NextSeq())))
		return semantic(t, tx).SetCurrentVersion("p1", "")
	})
	for _, tc := range []struct {
		name, item, prior string
		want              error
	}{
		{"first filing again", "p2", "", domain.ErrVersionConflict},
		{"stale expected prior", "p3", "p2", domain.ErrVersionConflict},
		{"missing item", "ghost", "p1", domain.ErrNotFound},
		{"duplicate occurrence", "dup", "p1", domain.ErrInvalidTransition},
		{"legacy inferred namespace", "legacy", "p1", domain.ErrInvalidRecord},
		{"expected prior is the item", "p1", "p1", domain.ErrInvalidTransition},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return semantic(t, tx).SetCurrentVersion(tc.item, tc.prior) })
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "p2-p1", domain.RelSupersedes, "p2", "p1", tx.NextSeq())))
		return semantic(t, tx).SetCurrentVersion("p2", "p1")
	})
	// A superseded item never becomes current again.
	rejected(t, s, sessA, domain.ErrInvalidTransition, func(tx store.Tx) error { return semantic(t, tx).SetCurrentVersion("p1", "p2") })
	view(t, s, sessA, func(tx store.ReadTx) error {
		if got := key(tx); got != "p2" {
			t.Errorf("current = %q, want p2", got)
		}
		return nil
	})
}

// testSemanticGrantsFor checks the indexed exact grant read (P3-5): typed
// targets match only their exact action and target, a legacy occurrence
// grant still names its item, a legacy stable-ID obligation grant never
// names an obligation version, and more matches than the limit fail.
func testSemanticGrantsFor(t *testing.T, s store.Store) {
	issuer := NewPrincipal(sessA, domain.AuthoritySystem)
	grant := func(id string, seq uint64, action domain.Action, targets ...domain.GrantTarget) domain.MutationGrant {
		g := NewGrant(sessA, id, seq)
		g.Issuer, g.Action, g.TargetIDs, g.Targets = issuer, action, nil, targets
		return g
	}
	item := domain.ItemGrantTarget(sessA, "i1")
	v1, v2 := domain.ObligationGrantTarget(sessA, "o1", 1), domain.ObligationGrantTarget(sessA, "o1", 2)
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertGrant(grant("g-item", tx.NextSeq(), domain.ActionResolve, item)))
		noErr(t, tx.InsertGrant(grant("g-v1", tx.NextSeq(), domain.ActionAssertObligation, v1)))
		legacyItem := NewGrant(sessA, "g-legacy-item", tx.NextSeq(), "i1")
		legacyItem.Issuer, legacyItem.Action = issuer, domain.ActionResolve
		noErr(t, tx.InsertGrant(legacyItem))
		legacyObl := NewGrant(sessA, "g-legacy-obl", tx.NextSeq(), "o1")
		legacyObl.Issuer, legacyObl.Action = issuer, domain.ActionAssertObligation
		return tx.InsertGrant(legacyObl)
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		ids := func(action domain.Action, target domain.GrantTarget, limit int) []string {
			gs, err := r.GrantsFor(action, target, limit)
			noErr(t, err)
			var out []string
			for _, g := range gs {
				out = append(out, g.ID)
			}
			return out
		}
		assertEqual(t, "GrantsFor(resolve, i1)", ids(domain.ActionResolve, item, 5), []string{"g-item", "g-legacy-item"})
		assertEqual(t, "GrantsFor(unpin, i1)", ids(domain.ActionUnpin, item, 5), []string(nil))
		assertEqual(t, "GrantsFor(assert, o1 v1)", ids(domain.ActionAssertObligation, v1, 5), []string{"g-v1"})
		assertEqual(t, "GrantsFor(assert, o1 v2)", ids(domain.ActionAssertObligation, v2, 5), []string(nil))
		_, err := r.GrantsFor(domain.ActionResolve, item, 1)
		wantErr(t, err, store.ErrLimitExceeded)
		_, err = r.GrantsFor(domain.ActionResolve, item, 0)
		wantErr(t, err, domain.ErrInvalidRecord)
		forged := item
		forged.AuthorizationKey = v1.AuthorizationKey
		_, err = r.GrantsFor(domain.ActionResolve, forged, 5)
		wantErr(t, err, domain.ErrInvalidRecord)
		_, err = r.GrantsFor(domain.ActionResolve, domain.ItemGrantTarget(sessB, "i1"), 5)
		wantErr(t, err, domain.ErrInvalidRecord)
		return nil
	})
}

// testSemanticGrantDuplicateTargets checks a legacy grant's TargetIDs are
// a set (DUR-1.10): a grant naming one item twice is rejected with
// ErrInvalidRecord by both stores, never a raw driver error or a doubled
// index entry.
func testSemanticGrantDuplicateTargets(t *testing.T, s store.Store) {
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
		g := NewGrant(sessA, "g-dup", tx.NextSeq(), "i1")
		g.Issuer, g.Action, g.TargetIDs = NewPrincipal(sessA, domain.AuthoritySystem), domain.ActionResolve, []string{"i1", "i1"}
		return tx.InsertGrant(g)
	})
}

// testSemanticChanges checks semantic change records (P3-36) and the
// indexed audit read by target: a change names an existing target and its
// audit event for that target, pages are access filtered before the limit,
// and LifecycleByTarget reads only its target's events.
func testSemanticChanges(t *testing.T, s store.Store) {
	actor := NewPrincipal(sessA, domain.AuthorityUser)
	target := domain.ItemGrantTarget(sessA, "p1")
	change := func(id string, seq uint64, audit string, access domain.AccessBoundary) domain.SemanticChange {
		return domain.SemanticChange{SemanticMeta: Meta(sessA, id, seq), Target: target, SourceAuthority: domain.AuthorityUser, Actor: actor, Access: access,
			Action: domain.ActionResolve, BeforeRevision: 1, AfterRevision: 2, BeforeStatus: "OPEN", AfterStatus: "RESOLVED",
			BeforeCurrentness: domain.ItemCurrent, AfterCurrentness: domain.ItemCurrent, AuditID: audit}
	}
	private := domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sessA, TaskID: "task", AgentID: "other"}
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(SemanticDirective(sessA, "p1", "dep", tx.NextSeq(), "p1")))
		noErr(t, tx.AppendLifecycleEvent(NewItemEvent(sessA, "l-other", tx.NextSeq(), "p9")))
		noErr(t, tx.AppendLifecycleEvent(NewItemEvent(sessA, "l1", tx.NextSeq(), "p1")))
		noErr(t, tx.AppendLifecycleEvent(NewItemEvent(sessA, "l2", tx.NextSeq(), "p1")))
		sem := semantic(t, tx)
		c1 := change("c1", tx.NextSeq(), "l1", DirectiveBoundary(sessA))
		c1.CauseID = "l2" // a stored audit event may be the cause
		noErr(t, sem.InsertSemanticChange(c1))
		return sem.InsertSemanticChange(change("c2", tx.NextSeq(), "l2", private))
	})
	for _, tc := range []struct {
		name string
		c    func(seq uint64) domain.SemanticChange
		want error
	}{
		{"reused ID", func(seq uint64) domain.SemanticChange { return change("c1", seq, "l1", DirectiveBoundary(sessA)) }, domain.ErrImmutable},
		{"missing audit", func(seq uint64) domain.SemanticChange { return change("c3", seq, "nope", DirectiveBoundary(sessA)) }, domain.ErrInvalidRecord},
		{"audit of another target", func(seq uint64) domain.SemanticChange { return change("c3", seq, "l-other", DirectiveBoundary(sessA)) }, domain.ErrInvalidRecord},
		{"missing target", func(seq uint64) domain.SemanticChange {
			c := change("c3", seq, "l1", DirectiveBoundary(sessA))
			c.Target = domain.ItemGrantTarget(sessA, "ghost")
			return c
		}, domain.ErrInvalidRecord},
		{"missing cause record", func(seq uint64) domain.SemanticChange {
			c := change("c3", seq, "l1", DirectiveBoundary(sessA))
			c.CauseID = "cause-missing"
			return c
		}, domain.ErrInvalidRecord},
		{"missing obligation version", func(seq uint64) domain.SemanticChange {
			c := change("c3", seq, "l1", DirectiveBoundary(sessA))
			c.Target = domain.ObligationGrantTarget(sessA, "o1", 1)
			return c
		}, domain.ErrInvalidRecord},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return semantic(t, tx).InsertSemanticChange(tc.c(tx.NextSeq())) })
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		owner := AgentPrincipal(sessA, "task", "agent")
		got, err := r.SemanticChanges(owner, target, store.Page{Limit: 5})
		noErr(t, err)
		if len(got.Records) != 1 || got.Records[0].ID != "c1" || got.More {
			t.Errorf("owner's changes = %+v, want c1 only (c2 is another agent's)", got)
		}
		all, err := r.SemanticChanges(AgentPrincipal(sessA, "task", "other"), target, store.Page{Limit: 1})
		noErr(t, err)
		if len(all.Records) != 1 || all.Records[0].ID != "c1" || !all.More {
			t.Errorf("other agent's first page = %+v, want c1 and More", all)
		}
		evs, err := r.LifecycleByTarget(domain.TargetItem, "p1", store.Page{Limit: 1})
		noErr(t, err)
		if len(evs.Records) != 1 || evs.Records[0].ID != "l1" || !evs.More {
			t.Fatalf("first lifecycle page = %+v, want l1 and More", evs)
		}
		evs, err = r.LifecycleByTarget(domain.TargetItem, "p1", store.Page{After: evs.Next, Limit: 5})
		noErr(t, err)
		if len(evs.Records) != 1 || evs.Records[0].ID != "l2" || evs.More {
			t.Errorf("second lifecycle page = %+v, want l2 only", evs)
		}
		return nil
	})
}
