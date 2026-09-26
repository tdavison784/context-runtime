package sqlite

import (
	"context"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestSessionsListsCommittedRecords(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.Update(ctx, "empty", func(tx store.Tx) error { tx.NextSeq(); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, "z-blob", func(tx store.Tx) error {
		data := []byte("stored independently")
		return tx.InsertBlob(domain.Blob{SessionID: "z-blob", Hash: domain.HashBytes(data), MediaType: "text/plain", Data: data})
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, "a-item", func(tx store.Tx) error {
		return tx.InsertItem(reviewItem("a-item", "i", tx.NextSeq()))
	}); err != nil {
		t.Fatal(err)
	}
	ids, err := s.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []string{"a-item", "z-blob"}) {
		t.Fatalf("Sessions = %v", ids)
	}
}

func reviewItem(session, id string, seq uint64) domain.ContextItem {
	parts := []domain.ContentPart{{Type: domain.PartText, Text: "review"}}
	return domain.ContextItem{ID: id, SessionID: session, Seq: seq, Kind: domain.KindFact,
		Generation: domain.GenerationWorking, Authority: domain.AuthorityUser,
		Scope: domain.ScopeSession, Access: domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: session},
		Residency: domain.ResidencyResident, Retention: domain.RetentionNormal,
		Parts: parts, ContentHash: domain.ContentHash(parts), SemanticBytes: domain.SemanticBytes(parts), Version: 1}
}
