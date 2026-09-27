package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
)

func TestSupersessionUsesReservedPlanWithoutEarlyWrites(t *testing.T) {
	s := memory.New()
	defer s.Close()
	err := s.Update(ctx, "s", func(tx store.Tx) error {
		actor := principal("s", domain.AuthorityUser)
		old := taskItem("s", "old", tx.NextSeq(), domain.AuthorityUser)
		fresh := taskItem("s", "new", tx.NextSeq(), domain.AuthorityUser)
		mustInsert(t, tx, old, fresh)
		p, err := planSupersession(tx, actor, fresh.ID, old.ID, "event", "")
		if err != nil {
			return err
		}
		if rs, err := tx.Relationships(store.RelationshipFilter{FromID: fresh.ID}); err != nil || len(rs) != 0 {
			t.Fatalf("planning wrote an edge: %v %v", rs, err)
		}
		tx.NextSeq() // other planned effects may reserve later sequences
		rel, err := applySupersession(tx, p)
		if err != nil {
			return err
		}
		if rel.Seq != p.rel.Seq || rel.Seq >= p.audit.Seq {
			t.Fatal("application changed reserved sequence order")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
