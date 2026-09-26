package storetest

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// richBlob is the blob richItem's image part references; insert it first.
func richBlob(sess string) domain.Blob { return NewBlob(sess, []byte("image bytes")) }

// richItem returns an item that uses every optional field.
func richItem(sess, id string, seq uint64) domain.ContextItem {
	blob := richBlob(sess)
	parts := []domain.ContentPart{
		{Type: domain.PartText, MediaType: "text/plain", Text: "caption"},
		{Type: domain.PartImage, MediaType: "image/png", BlobHash: blob.Hash, BlobSize: uint64(len(blob.Data))},
	}
	it := NewGoal(sess, id, seq, "")
	it.DirectiveID = "goal-1"
	it.Parts = parts
	it.ContentHash = domain.ContentHash(parts)
	it.SemanticBytes = domain.SemanticBytes(parts)
	it.Scope = domain.ScopeAgent
	it.Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sess, TaskID: "task", AgentID: "agent"}
	it.Generation = domain.GenerationPinned
	it.Authority = domain.AuthorityHarness
	it.Retention = domain.RetentionProtected
	it.Importance = -42
	it.LastUsedCall = 7
	it.AccessCount = 3
	ttl := 5
	it.TTLTurns = &ttl
	it.Tags = []string{"a", "b"}
	it.Source = &domain.SourceRef{Kind: domain.SourcePath, Locator: "/x", ContentHash: blob.Hash, ToolCallID: "tc"}
	return it
}

func testItemRichRoundTrip(t *testing.T, s store.Store) {
	var want domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		want = richItem(sessA, "rich", tx.NextSeq())
		noErr(t, tx.InsertBlob(richBlob(sessA)))
		noErr(t, tx.InsertItem(want))
		got, err := tx.Item("rich")
		noErr(t, err)
		assertEqual(t, "Item inside Update", got, want)
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Item("rich")
		noErr(t, err)
		assertEqual(t, "Item", got, want)
		items, err := tx.Items(store.ItemFilter{})
		noErr(t, err)
		assertEqual(t, "Items", items, []domain.ContextItem{want})
		return nil
	})
}

func testItemInsertRules(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		noErr(t, tx.InsertItem(NewItem(sessA, "i1", seq, "one")))
		return nil
	})
	cases := []struct {
		name   string
		mutate func(it *domain.ContextItem)
		want   error
	}{
		{"version 2", func(it *domain.ContextItem) { it.Version = 2 }, domain.ErrInvalidRecord},
		{"version 0", func(it *domain.ContextItem) { it.Version = 0 }, domain.ErrInvalidRecord},
		{"seq 0", func(it *domain.ContextItem) { it.Seq = 0 }, domain.ErrInvalidRecord},
		{"unallocated seq", func(it *domain.ContextItem) { it.Seq = 1000 }, domain.ErrInvalidRecord},
		{"seq from an earlier transaction", func(it *domain.ContextItem) { it.Seq = 1 }, domain.ErrInvalidRecord},
		{"wrong content hash", func(it *domain.ContextItem) { it.ContentHash = domain.HashBytes(nil) }, domain.ErrInvalidRecord},
		{"wrong semantic bytes", func(it *domain.ContextItem) { it.SemanticBytes++ }, domain.ErrInvalidRecord},
		{"invalid kind", func(it *domain.ContextItem) { it.Kind = "bogus" }, domain.ErrInvalidRecord},
		{"goal without status", func(it *domain.ContextItem) { it.Kind = domain.KindGoal }, domain.ErrInvalidRecord},
		{"reused ID", func(it *domain.ContextItem) { it.ID = "i1" }, domain.ErrImmutable},
		{"reused ID, identical content", func(it *domain.ContextItem) { *it = NewItem(sessA, "i1", it.Seq, "one") }, domain.ErrImmutable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Update(ctx, sessA, func(tx store.Tx) error {
				it := NewItem(sessA, "new", tx.NextSeq(), "new")
				tc.mutate(&it)
				return tx.InsertItem(it)
			})
			wantErr(t, err, tc.want)
		})
	}
	// Reuse within the inserting transaction is rejected too.
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(NewItem(sessA, "i2", tx.NextSeq(), "two")))
		return tx.InsertItem(NewItem(sessA, "i2", tx.NextSeq(), "two again"))
	})
	wantErr(t, err, domain.ErrImmutable)
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Item("i1")
		noErr(t, err)
		assertEqual(t, "Item i1", got, NewItem(sessA, "i1", 1, "one"))
		_, err = tx.Item("new")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

func testItemsFilterOrder(t *testing.T, s store.Store) {
	var all []domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		n := seqs(tx, 6)
		mk := func(id string, seq uint64, edit func(*domain.ContextItem)) domain.ContextItem {
			it := NewItem(sessA, id, seq, id)
			if edit != nil {
				edit(&it)
			}
			return it
		}
		all = []domain.ContextItem{
			mk("a", n[0], nil),
			mk("b", n[1], func(it *domain.ContextItem) { it.TaskID, it.AgentID = "task2", "agent2" }),
			mk("c", n[2], func(it *domain.ContextItem) { it.Residency = domain.ResidencyArchived }),
			NewDirective(sessA, "d", "dir", n[3], "d"),
			// Two items sharing a sequence number sort by ID.
			mk("f", n[4], func(it *domain.ContextItem) { it.EventID = "shared" }),
			mk("e", n[4], func(it *domain.ContextItem) { it.EventID = "shared"; it.Kind = domain.KindDecision }),
			mk("g", n[5], nil),
		}
		// Insert in reverse so results cannot rely on insertion order.
		for _, it := range slices.Backward(all) {
			noErr(t, tx.InsertItem(it))
		}
		return nil
	})
	byID := map[string]domain.ContextItem{}
	for _, it := range all {
		byID[it.ID] = it
	}
	cases := []struct {
		name string
		f    store.ItemFilter
		want []string
	}{
		{"all", store.ItemFilter{}, []string{"a", "b", "c", "d", "e", "f", "g"}},
		{"task", store.ItemFilter{TaskID: "task2"}, []string{"b"}},
		{"agent", store.ItemFilter{AgentID: "agent"}, []string{"a", "c", "d", "e", "f", "g"}},
		{"kinds", store.ItemFilter{Kinds: []domain.Kind{domain.KindDecision, domain.KindInstruction}}, []string{"d", "e"}},
		{"residency", store.ItemFilter{Residency: domain.ResidencyArchived}, []string{"c"}},
		{"directive", store.ItemFilter{DirectiveID: "dir"}, []string{"d"}},
		{"event", store.ItemFilter{EventID: "shared"}, []string{"e", "f"}},
		{"min seq inclusive", store.ItemFilter{MinSeq: 5}, []string{"e", "f", "g"}},
		{"max seq inclusive", store.ItemFilter{MaxSeq: 2}, []string{"a", "b"}},
		{"seq range", store.ItemFilter{MinSeq: 3, MaxSeq: 4}, []string{"c", "d"}},
		{"conjunction", store.ItemFilter{AgentID: "agent", MinSeq: 5, Kinds: []domain.Kind{domain.KindFact}}, []string{"f", "g"}},
		{"no match", store.ItemFilter{TaskID: "nope"}, nil},
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		for _, tc := range cases {
			got, err := tx.Items(tc.f)
			noErr(t, err)
			var want []domain.ContextItem
			for _, id := range tc.want {
				want = append(want, byID[id])
			}
			assertEqual(t, "Items "+tc.name, got, want)
		}
		return nil
	})
}

// blobItem returns a fact whose single document part references b with
// the given size.
func blobItem(sess, id string, seq uint64, b domain.Blob, size uint64) domain.ContextItem {
	it := NewItem(sess, id, seq, "")
	it.Parts = []domain.ContentPart{{Type: domain.PartDocument, MediaType: "application/pdf", BlobHash: b.Hash, BlobSize: size}}
	it.ContentHash = domain.ContentHash(it.Parts)
	it.SemanticBytes = domain.SemanticBytes(it.Parts)
	return it
}

// testItemBlobIntegrity checks FR-ING-007 at insertion: every blob part
// references a blob stored in this session with the declared size.
func testItemBlobIntegrity(t *testing.T, s store.Store) {
	b := NewBlob(sessA, []byte("document bytes"))
	size := uint64(len(b.Data))
	update(t, s, sessB, func(tx store.Tx) error { return tx.InsertBlob(NewBlob(sessB, b.Data)) })
	cases := []struct {
		name      string
		storeBlob bool
		size      uint64
		want      error
	}{
		{"missing blob", false, size, domain.ErrIntegrity},
		{"blob only in another session", false, size, domain.ErrIntegrity},
		{"size too small", true, size - 1, domain.ErrIntegrity},
		{"size too large", true, size + 1, domain.ErrIntegrity},
		{"stored earlier in the transaction", true, size, nil},
	}
	for _, tc := range cases {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			if tc.storeBlob {
				noErr(t, tx.InsertBlob(b))
			}
			if err := tx.InsertItem(blobItem(sessA, "doc", tx.NextSeq(), b, tc.size)); err != nil {
				return err
			}
			return errRollback
		})
		if tc.want == nil {
			tc.want = errRollback
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	// A blob committed by an earlier transaction satisfies the part.
	update(t, s, sessA, func(tx store.Tx) error { return tx.InsertBlob(b) })
	update(t, s, sessA, func(tx store.Tx) error {
		return tx.InsertItem(blobItem(sessA, "doc", tx.NextSeq(), b, size))
	})
}

func testUpdateItem(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		return tx.InsertItem(NewItem(sessA, "i1", tx.NextSeq(), "one"))
	})
	durable, archived, low, call := domain.GenerationDurable, domain.ResidencyArchived, domain.RetentionLow, uint64(9)
	var updated domain.ContextItem
	var audit domain.LifecycleEvent
	update(t, s, sessA, func(tx store.Tx) error {
		n := seqs(tx, 4)
		_, err := tx.UpdateItem("missing", 1, domain.ItemChange{}, NewItemEvent(sessA, "l0", n[0], "missing"))
		wantErr(t, err, domain.ErrNotFound)
		_, err = tx.UpdateItem("i1", 2, domain.ItemChange{AccessDelta: 1}, NewItemEvent(sessA, "l0", n[0], "i1"))
		wantErr(t, err, domain.ErrVersionConflict)

		audit = NewItemEvent(sessA, "l1", n[1], "i1")
		updated, err = tx.UpdateItem("i1", 1, domain.ItemChange{
			Generation: &durable, Residency: &archived, Retention: &low, LastUsedCall: &call, AccessDelta: 2,
		}, audit)
		noErr(t, err)
		want := NewItem(sessA, "i1", 1, "one")
		want.Generation, want.Residency, want.Retention = durable, archived, low
		want.LastUsedCall, want.AccessCount, want.Version = 9, 2, 2
		assertEqual(t, "UpdateItem result", updated, want)
		got, err := tx.Item("i1")
		noErr(t, err)
		assertEqual(t, "Item after UpdateItem", got, want)
		evs, err := tx.LifecycleEvents(store.LifecycleFilter{TargetID: "i1"})
		noErr(t, err)
		assertEqual(t, "audit event inside Update", evs, []domain.LifecycleEvent{audit})

		// The stale version now conflicts.
		_, err = tx.UpdateItem("i1", 1, domain.ItemChange{AccessDelta: 1}, NewItemEvent(sessA, "l2", n[2], "i1"))
		wantErr(t, err, domain.ErrVersionConflict)
		return nil
	})
	bad := domain.Generation("bogus")
	earlier := uint64(3)
	open := domain.GoalOpen
	valid := func(id string, seq uint64) domain.LifecycleEvent { return NewItemEvent(sessA, id, seq, "i1") }
	cases := []struct {
		name   string
		change domain.ItemChange
		event  func(seq uint64) domain.LifecycleEvent
		want   error
	}{
		{"invalid generation", domain.ItemChange{Generation: &bad}, func(seq uint64) domain.LifecycleEvent { return valid("lx", seq) }, domain.ErrInvalidRecord},
		{"negative access delta", domain.ItemChange{AccessDelta: -1}, func(seq uint64) domain.LifecycleEvent { return valid("lx", seq) }, domain.ErrInvalidTransition},
		{"last used call regresses", domain.ItemChange{LastUsedCall: &earlier}, func(seq uint64) domain.LifecycleEvent { return valid("lx", seq) }, domain.ErrInvalidTransition},
		{"goal status on a non-goal", domain.ItemChange{GoalStatus: &open}, func(seq uint64) domain.LifecycleEvent { return valid("lx", seq) }, domain.ErrInvalidTransition},
		{"event targets another item", domain.ItemChange{AccessDelta: 1}, func(seq uint64) domain.LifecycleEvent {
			return NewItemEvent(sessA, "lx", seq, "i2")
		}, domain.ErrInvalidRecord},
		{"event targets another kind", domain.ItemChange{AccessDelta: 1}, func(seq uint64) domain.LifecycleEvent {
			return NewLifecycleEvent(sessA, "lx", seq, domain.TargetObligation, "i1")
		}, domain.ErrInvalidRecord},
		{"event seq from an earlier transaction", domain.ItemChange{AccessDelta: 1}, func(uint64) domain.LifecycleEvent {
			return valid("lx", 1)
		}, domain.ErrInvalidRecord},
		{"event ID reused", domain.ItemChange{AccessDelta: 1}, func(seq uint64) domain.LifecycleEvent { return valid("l1", seq) }, domain.ErrImmutable},
		{"invalid event", domain.ItemChange{AccessDelta: 1}, func(seq uint64) domain.LifecycleEvent {
			e := valid("lx", seq)
			e.Action = ""
			return e
		}, domain.ErrInvalidRecord},
	}
	for _, tc := range cases {
		// The rejected write leaves the transaction untouched, so committing
		// afterwards must store neither the change nor the event.
		update(t, s, sessA, func(tx store.Tx) error {
			_, err := tx.UpdateItem("i1", 2, tc.change, tc.event(tx.NextSeq()))
			if !errors.Is(err, tc.want) {
				t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
			}
			return nil
		})
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Item("i1")
		noErr(t, err)
		assertEqual(t, "Item after rejected changes", got, updated)
		evs, err := tx.LifecycleEvents(store.LifecycleFilter{})
		noErr(t, err)
		assertEqual(t, "LifecycleEvents after rejected changes", evs, []domain.LifecycleEvent{audit})
		return nil
	})
}

// testGoalLifecycle follows trace T05: resolution, archival, and retrieval
// never reopen a goal, and residency changes preserve goal status.
func testGoalLifecycle(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		return tx.InsertItem(NewGoal(sessA, "g", tx.NextSeq(), "ship it"))
	})
	resolved, open := domain.GoalResolved, domain.GoalOpen
	archived, resident := domain.ResidencyArchived, domain.ResidencyResident
	steps := []struct {
		name       string
		change     domain.ItemChange
		want       error
		status     domain.GoalStatus
		residency  domain.Residency
		newVersion uint64
	}{
		{"resolve", domain.ItemChange{GoalStatus: &resolved}, nil, resolved, resident, 2},
		{"archive", domain.ItemChange{Residency: &archived}, nil, resolved, archived, 3},
		{"retrieve", domain.ItemChange{Residency: &resident, AccessDelta: 1}, nil, resolved, resident, 4},
		{"reopen", domain.ItemChange{GoalStatus: &open}, domain.ErrInvalidTransition, resolved, resident, 4},
		{"reopen while archiving", domain.ItemChange{Residency: &archived, GoalStatus: &open}, domain.ErrInvalidTransition, resolved, resident, 4},
		{"resolve again is a no-op status change", domain.ItemChange{GoalStatus: &resolved}, nil, resolved, resident, 5},
	}
	version := uint64(1)
	for i, st := range steps {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			_, err := tx.UpdateItem("g", version, st.change, NewItemEvent(sessA, fmt.Sprintf("l%d", i), tx.NextSeq(), "g"))
			return err
		})
		if !errors.Is(err, st.want) {
			t.Fatalf("%s: error = %v, want %v", st.name, err, st.want)
		}
		view(t, s, sessA, func(tx store.ReadTx) error {
			g, err := tx.Item("g")
			noErr(t, err)
			if g.GoalStatus == nil || *g.GoalStatus != st.status || g.Residency != st.residency || g.Version != st.newVersion {
				t.Errorf("%s: goal = (status %v, residency %s, version %d), want (%s, %s, %d)",
					st.name, g.GoalStatus, g.Residency, g.Version, st.status, st.residency, st.newVersion)
			}
			version = g.Version
			return nil
		})
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		evs, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetItem, TargetID: "g"})
		noErr(t, err)
		if len(evs) != 4 {
			t.Errorf("audit events for g = %d, want 4 (one per applied change)", len(evs))
		}
		return nil
	})
}

func testRelationships(t *testing.T, s store.Store) {
	var want []domain.Relationship
	update(t, s, sessA, func(tx store.Tx) error {
		n := seqs(tx, 5)
		for i, id := range []string{"a", "b", "c"} {
			noErr(t, tx.InsertItem(NewItem(sessA, id, n[i], id)))
		}
		derived := NewRelationship(sessA, "r2", domain.RelDerivedFrom, "c", "a", n[3])
		derived.RuleVersion = "rule/v1"
		derived.Coverage = &domain.Coverage{ConversationID: "conv", FromSeq: 1, ToSeq: 3}
		want = []domain.Relationship{
			NewRelationship(sessA, "r3", domain.RelSupersedes, "b", "a", n[3]),
			derived,
			NewRelationship(sessA, "r1", domain.RelReferences, "c", "b", n[4]),
		}
		// Insert out of order: results sort by Seq, then ID.
		for _, r := range slices.Backward(want) {
			noErr(t, tx.InsertRelationship(r))
		}
		slices.SortFunc(want, func(x, y domain.Relationship) int {
			return cmp.Or(cmp.Compare(x.Seq, y.Seq), cmp.Compare(x.ID, y.ID))
		})
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Relationships(store.RelationshipFilter{})
		noErr(t, err)
		assertEqual(t, "Relationships", got, want)
		cases := []struct {
			f    store.RelationshipFilter
			want []string
		}{
			{store.RelationshipFilter{Type: domain.RelSupersedes}, []string{"r3"}},
			{store.RelationshipFilter{FromID: "c"}, []string{"r2", "r1"}},
			{store.RelationshipFilter{ToID: "a"}, []string{"r2", "r3"}},
			{store.RelationshipFilter{FromID: "c", ToID: "b"}, []string{"r1"}},
			{store.RelationshipFilter{Type: domain.RelDuplicateOf}, nil},
		}
		for _, tc := range cases {
			got, err := tx.Relationships(tc.f)
			noErr(t, err)
			var ids []string
			for _, r := range got {
				ids = append(ids, r.ID)
			}
			if !slices.Equal(ids, tc.want) {
				t.Errorf("Relationships(%+v) = %v, want %v", tc.f, ids, tc.want)
			}
		}
		return nil
	})
	cases := []struct {
		name string
		r    func(seq uint64) domain.Relationship
		want error
	}{
		{"missing from", func(seq uint64) domain.Relationship {
			return NewRelationship(sessA, "x", domain.RelDerivedFrom, "nope", "a", seq)
		}, domain.ErrDanglingRelationship},
		{"missing to", func(seq uint64) domain.Relationship {
			return NewRelationship(sessA, "x", domain.RelSupersedes, "a", "nope", seq)
		}, domain.ErrDanglingRelationship},
		{"reused ID", func(seq uint64) domain.Relationship {
			return NewRelationship(sessA, "r1", domain.RelReferences, "a", "c", seq)
		}, domain.ErrImmutable},
		{"invalid type", func(seq uint64) domain.Relationship {
			return NewRelationship(sessA, "x", "BOGUS", "a", "b", seq)
		}, domain.ErrInvalidRecord},
		{"self edge", func(seq uint64) domain.Relationship {
			return NewRelationship(sessA, "x", domain.RelDerivedFrom, "a", "a", seq)
		}, domain.ErrInvalidRecord},
		{"self supersession", func(seq uint64) domain.Relationship {
			return NewRelationship(sessA, "x", domain.RelSupersedes, "a", "a", seq)
		}, domain.ErrSupersessionCycle},
		{"inverted coverage", func(seq uint64) domain.Relationship {
			r := NewRelationship(sessA, "x", domain.RelDerivedFrom, "a", "b", seq)
			r.Coverage = &domain.Coverage{FromSeq: 3, ToSeq: 1}
			return r
		}, domain.ErrInvalidRecord},
	}
	for _, tc := range cases {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return tx.InsertRelationship(tc.r(tx.NextSeq())) })
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// testSupersessionAcyclic checks FR-REL-004: only SUPERSEDES edges are
// checked for cycles, across transactions and through long chains.
func testSupersessionAcyclic(t *testing.T, s store.Store) {
	const chain = 200
	update(t, s, sessA, func(tx store.Tx) error {
		for i := range chain {
			noErr(t, tx.InsertItem(NewItem(sessA, fmt.Sprintf("n%03d", i), tx.NextSeq(), "x")))
		}
		// n001 SUPERSEDES n000, n002 SUPERSEDES n001, ...
		for i := 1; i < chain; i++ {
			noErr(t, tx.InsertRelationship(NewRelationship(sessA, fmt.Sprintf("s%03d", i),
				domain.RelSupersedes, fmt.Sprintf("n%03d", i), fmt.Sprintf("n%03d", i-1), tx.NextSeq())))
		}
		return nil
	})
	edge := func(id string, typ domain.RelationshipType, from, to string) error {
		return s.Update(ctx, sessA, func(tx store.Tx) error {
			return tx.InsertRelationship(NewRelationship(sessA, id, typ, from, to, tx.NextSeq()))
		})
	}
	last := fmt.Sprintf("n%03d", chain-1)
	wantErr(t, edge("back1", domain.RelSupersedes, "n000", "n001"), domain.ErrSupersessionCycle)
	wantErr(t, edge("backN", domain.RelSupersedes, "n000", last), domain.ErrSupersessionCycle)
	wantErr(t, edge("mid", domain.RelSupersedes, "n050", "n150"), domain.ErrSupersessionCycle)
	// Forward shortcuts and diamonds keep the graph acyclic.
	noErr(t, edge("skip", domain.RelSupersedes, last, "n000"))
	noErr(t, edge("dup", domain.RelSupersedes, "n010", "n009"))
	// Other relationship types are not checked for cycles and do not count
	// toward supersession cycles.
	noErr(t, edge("d1", domain.RelDerivedFrom, "n000", last))
	noErr(t, edge("d2", domain.RelDependsOn, "n000", "n001"))
	noErr(t, edge("d3", domain.RelDependsOn, "n001", "n000"))

	// A cycle closed within one transaction is rejected as well.
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		n := seqs(tx, 4)
		noErr(t, tx.InsertItem(NewItem(sessA, "p", n[0], "p")))
		noErr(t, tx.InsertItem(NewItem(sessA, "q", n[1], "q")))
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "pq", domain.RelSupersedes, "p", "q", n[2])))
		return tx.InsertRelationship(NewRelationship(sessA, "qp", domain.RelSupersedes, "q", "p", n[3]))
	})
	wantErr(t, err, domain.ErrSupersessionCycle)

	view(t, s, sessA, func(tx store.ReadTx) error {
		rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes})
		noErr(t, err)
		if len(rels) != chain+1 {
			t.Errorf("SUPERSEDES edges = %d, want %d", len(rels), chain+1)
		}
		return nil
	})
}

func testEvents(t *testing.T, s store.Store) {
	orig := NewEvent(sessA, "e1", 0, "payload")
	orig.SourceHash = domain.HashBytes([]byte("source"))
	update(t, s, sessA, func(tx store.Tx) error {
		orig.Seq = tx.NextSeq()
		orig.ItemIDs = []string{"x", "y"}
		stored, existed, err := tx.InsertEvent(orig)
		noErr(t, err)
		if existed {
			t.Errorf("first InsertEvent existed = true")
		}
		assertEqual(t, "InsertEvent result", stored, orig)
		return nil
	})
	// A retry with the same request returns the stored record, even though
	// the caller's outcome fields differ, and writes nothing.
	update(t, s, sessA, func(tx store.Tx) error {
		retry := orig
		retry.Seq = 1
		retry.ItemIDs = []string{"z"}
		retry.CommittedAt = T0.Add(1)
		stored, existed, err := tx.InsertEvent(retry)
		noErr(t, err)
		if !existed {
			t.Errorf("retried InsertEvent existed = false")
		}
		assertEqual(t, "retried InsertEvent result", stored, orig)
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Event("e1")
		noErr(t, err)
		assertEqual(t, "Event", got, orig)
		if tx.LastSeq() != 1 {
			t.Errorf("LastSeq = %d, want 1", tx.LastSeq())
		}
		_, err = tx.Event("missing")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
	conflicts := []struct {
		name string
		edit func(e *domain.EventRecord)
	}{
		{"workflow", func(e *domain.EventRecord) { e.Principal.WorkflowID = "other" }},
		{"task", func(e *domain.EventRecord) { e.Principal.TaskID = "other" }},
		{"agent", func(e *domain.EventRecord) { e.Principal.AgentID = "other" }},
		{"authority", func(e *domain.EventRecord) { e.Principal.Authority = domain.AuthorityAgent }},
		{"payload", func(e *domain.EventRecord) { e.PayloadHash = domain.HashBytes([]byte("other")) }},
		{"source", func(e *domain.EventRecord) { e.SourceHash = domain.HashBytes([]byte("other")) }},
		{"no source", func(e *domain.EventRecord) { e.SourceHash = "" }},
	}
	for _, tc := range conflicts {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			e := orig
			tc.edit(&e)
			_, _, err := tx.InsertEvent(e)
			return err
		})
		if !errors.Is(err, domain.ErrEventIDConflict) {
			t.Errorf("conflicting %s: error = %v, want ErrEventIDConflict", tc.name, err)
		}
	}
	invalids := []struct {
		name string
		edit func(e *domain.EventRecord)
	}{
		{"malformed payload hash", func(e *domain.EventRecord) { e.PayloadHash = "md5:x" }},
		{"missing seq", func(e *domain.EventRecord) { e.Seq = 0 }},
		{"seq from an earlier transaction", func(e *domain.EventRecord) { e.Seq = 1 }},
		{"invalid principal", func(e *domain.EventRecord) { e.Principal.Authority = "" }},
	}
	for _, tc := range invalids {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			e := NewEvent(sessA, "e2", tx.NextSeq(), "p")
			tc.edit(&e)
			_, _, err := tx.InsertEvent(e)
			return err
		})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("%s: error = %v, want ErrInvalidRecord", tc.name, err)
		}
	}
}

func testBlobs(t *testing.T, s store.Store) {
	b := NewBlob(sessA, []byte("snapshot B1"))
	empty := NewBlob(sessA, nil)
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertBlob(b))
		noErr(t, tx.InsertBlob(b)) // identical bytes: no-op
		noErr(t, tx.InsertBlob(empty))
		return nil
	})
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertBlob(b))
		corrupt := b
		corrupt.Data = []byte("snapshot B2")
		wantErr(t, tx.InsertBlob(corrupt), domain.ErrIntegrity)
		malformed := NewBlob(sessA, []byte("x"))
		malformed.Hash = "sha256:XYZ"
		wantErr(t, tx.InsertBlob(malformed), domain.ErrInvalidRecord)
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Blob(b.Hash)
		noErr(t, err)
		assertEqual(t, "Blob", got, b)
		got, err = tx.Blob(empty.Hash)
		noErr(t, err)
		assertEqual(t, "empty Blob", got, empty)
		_, err = tx.Blob(domain.HashBytes([]byte("snapshot B2")))
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
	// Blobs are per session: the same bytes are invisible elsewhere until
	// that session stores them itself.
	view(t, s, sessB, func(tx store.ReadTx) error {
		_, err := tx.Blob(b.Hash)
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
	update(t, s, sessB, func(tx store.Tx) error { return tx.InsertBlob(NewBlob(sessB, b.Data)) })
	view(t, s, sessB, func(tx store.ReadTx) error {
		got, err := tx.Blob(b.Hash)
		noErr(t, err)
		assertEqual(t, "Blob in session B", got, NewBlob(sessB, b.Data))
		return nil
	})
}

// testDirectiveReplacement follows trace T02: P2 SUPERSEDES P1, the current
// map points at P2, P1 is retained unchanged, and P1's obligation version
// is retired while P2's starts UNRESOLVED.
func testDirectiveReplacement(t *testing.T, s store.Store) {
	var p1, p2 domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		p1 = NewDirective(sessA, "p1", "dep", tx.NextSeq(), "Use dependency v2.")
		noErr(t, tx.InsertItem(p1))
		noErr(t, tx.SetCurrentDirective("task", "dep", "p1"))
		noErr(t, tx.InsertObligationVersion(NewObligation(sessA, "obl", 1, p1.Seq, "p1")))
		return nil
	})
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		p2 = NewDirective(sessA, "p2", "dep", seq, "Use dependency v3.")
		noErr(t, tx.InsertItem(p2))
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "p2-p1", domain.RelSupersedes, "p2", "p1", seq)))
		noErr(t, tx.SetCurrentDirective("task", "dep", "p2"))
		old, err := tx.Obligation("obl")
		noErr(t, err)
		old.Current, old.RetiredSeq = false, seq
		_, err = tx.UpdateObligationVersion(old, old.Revision)
		noErr(t, err)
		noErr(t, tx.InsertObligationVersion(NewObligation(sessA, "obl", 2, seq, "p2")))
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		cur, err := tx.CurrentDirective("task", "dep")
		noErr(t, err)
		if cur != "p2" {
			t.Errorf("CurrentDirective = %q, want p2", cur)
		}
		got, err := tx.Item("p1")
		noErr(t, err)
		assertEqual(t, "retained P1", got, p1)
		rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, ToID: "p1"})
		noErr(t, err)
		if len(rels) != 1 || rels[0].FromID != "p2" {
			t.Errorf("SUPERSEDES edges into p1 = %+v, want one from p2", rels)
		}
		items, err := tx.Items(store.ItemFilter{DirectiveID: "dep"})
		noErr(t, err)
		if len(items) != 2 {
			t.Errorf("items with directive dep = %d, want 2", len(items))
		}
		vers, err := tx.ObligationVersions("obl")
		noErr(t, err)
		if len(vers) != 2 || vers[0].Current || vers[0].RetiredSeq != p2.Seq || !vers[1].Current ||
			vers[1].Status != domain.ObligationUnresolved || vers[1].SourceItemID != "p2" {
			t.Errorf("obligation versions = %+v, want v1 retired at %d and v2 current UNRESOLVED from p2", vers, p2.Seq)
		}
		return nil
	})
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		wantErr(t, tx.SetCurrentDirective("task", "dep", "missing"), domain.ErrNotFound)
		noErr(t, tx.InsertItem(NewItem(sessA, "plain", tx.NextSeq(), "no directive")))
		wantErr(t, tx.SetCurrentDirective("task", "dep", "plain"), domain.ErrInvalidRecord)
		wantErr(t, tx.SetCurrentDirective("task", "other", "p2"), domain.ErrInvalidRecord)
		// The item must belong to the task it becomes current in.
		wantErr(t, tx.SetCurrentDirective("task2", "dep", "p2"), domain.ErrInvalidRecord)
		wantErr(t, tx.SetCurrentDirective("", "dep", "p2"), domain.ErrInvalidRecord)
		wantErr(t, tx.SetCurrentDirective("task", "", "p2"), domain.ErrInvalidRecord)
		_, err := tx.CurrentDirective("task", "other")
		wantErr(t, err, domain.ErrNotFound)
		_, err = tx.CurrentDirective("task2", "dep")
		wantErr(t, err, domain.ErrNotFound)
		// Moving the pointer back is permitted; the store does not judge
		// which version is current.
		noErr(t, tx.SetCurrentDirective("task", "dep", "p1"))
		cur, err := tx.CurrentDirective("task", "dep")
		noErr(t, err)
		if cur != "p1" {
			t.Errorf("CurrentDirective = %q, want p1", cur)
		}
		return errRollback
	})
	wantErr(t, err, errRollback)
}

// testRelationshipFilters checks filtered relationship reads across
// committed edges and a transaction's own edges, and that a rolled-back
// transaction's edges never appear. Stores may answer filters from indexes;
// the results must equal a full scan's.
func testRelationshipFilters(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		for _, id := range []string{"a", "b", "c", "d"} {
			noErr(t, tx.InsertItem(NewItem(sessA, id, tx.NextSeq(), id)))
		}
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "e1", domain.RelSupersedes, "a", "b", tx.NextSeq())))
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "e2", domain.RelDerivedFrom, "a", "c", tx.NextSeq())))
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "e3", domain.RelReferences, "c", "b", tx.NextSeq())))
		return nil
	})
	cases := []struct {
		f    store.RelationshipFilter
		want []string
	}{
		{store.RelationshipFilter{FromID: "a"}, []string{"e1", "e2", "e5"}},
		{store.RelationshipFilter{ToID: "b"}, []string{"e1", "e3", "e4"}},
		{store.RelationshipFilter{Type: domain.RelSupersedes}, []string{"e1", "e4"}},
		{store.RelationshipFilter{FromID: "a", Type: domain.RelReferences}, []string{"e5"}},
		{store.RelationshipFilter{FromID: "a", ToID: "c"}, []string{"e2"}},
		{store.RelationshipFilter{ToID: "b", Type: domain.RelReferences}, []string{"e3"}},
		{store.RelationshipFilter{FromID: "b"}, nil},
		{store.RelationshipFilter{}, []string{"e1", "e2", "e3", "e4", "e5"}},
	}
	check := func(tx store.ReadTx, when string) {
		t.Helper()
		for _, tc := range cases {
			got, err := tx.Relationships(tc.f)
			noErr(t, err)
			var ids []string
			for _, r := range got {
				ids = append(ids, r.ID)
			}
			if !slices.Equal(ids, tc.want) {
				t.Errorf("%s: Relationships(%+v) = %v, want %v", when, tc.f, ids, tc.want)
			}
		}
	}
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "e4", domain.RelSupersedes, "d", "b", tx.NextSeq())))
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "e5", domain.RelReferences, "a", "d", tx.NextSeq())))
		check(tx, "inside the writing transaction")
		return nil
	})
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "e6", domain.RelDuplicateOf, "a", "b", tx.NextSeq())))
		noErr(t, tx.InsertRelationship(NewRelationship(sessA, "e7", domain.RelSupersedes, "b", "c", tx.NextSeq())))
		return errRollback
	})
	wantErr(t, err, errRollback)
	view(t, s, sessA, func(tx store.ReadTx) error {
		check(tx, "after commit and rollback")
		return nil
	})
	// The rolled-back SUPERSEDES edge b -> c must not count toward cycles.
	update(t, s, sessA, func(tx store.Tx) error {
		return tx.InsertRelationship(NewRelationship(sessA, "e8", domain.RelSupersedes, "c", "b", tx.NextSeq()))
	})
}
