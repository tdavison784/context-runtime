package lifecycle

import (
	"context"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_37_NoContentDeletionOnArchive closes the P3-42 table row "no content
// deletion": archival changes residency only — every content part, the
// content hash, semantic size, kind and authority survive an explicit
// archive, remain fully retrievable while archived, and round-trip through
// unarchive unchanged.
func TestP3_37_NoContentDeletionOnArchive(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		it := storetest.NewItem("s", "fact", 0, "line one")
		it.Parts = append(it.Parts, domain.ContentPart{Type: domain.PartText, MediaType: "text/plain", Text: "line two"})
		it.ContentHash = domain.ContentHash(it.Parts)
		it.SemanticBytes = domain.SemanticBytes(it.Parts)
		seedItem(t, db, it)
		s, _ := New(db, testPolicy())
		p := storetest.NewPrincipal("s", domain.AuthorityUser)

		// Content is fully retrievable while archived.
		if out, err := s.ArchiveStandalone(ctx, p, domain.ArchiveIntent{RequestID: "a", ItemID: "fact", ExpectedVersion: 1}); err != nil || out.After.Residency != domain.ResidencyArchived {
			t.Fatalf("archive: %+v %v", out, err)
		}
		check := func(version uint64) {
			t.Helper()
			if err := db.View(ctx, "s", func(tx store.ReadTx) error {
				got, err := tx.Item("fact")
				if err != nil {
					return err
				}
				if got.Residency != domain.ResidencyArchived || got.Version != version {
					t.Fatalf("archived item: %+v", got)
				}
				if !slices.EqualFunc(got.Parts, it.Parts, func(a, b domain.ContentPart) bool { return a == b }) {
					t.Fatalf("parts deleted or altered: %+v", got.Parts)
				}
				if got.ContentHash != it.ContentHash || got.SemanticBytes != it.SemanticBytes || got.Kind != it.Kind || got.Authority != it.Authority {
					t.Fatalf("content identity altered: %+v", got)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		check(2)

		// And after unarchive the content is byte-identical again.
		if _, err := s.UnarchiveStandalone(ctx, p, domain.UnarchiveIntent{RequestID: "u", ItemID: "fact", ExpectedVersion: 2}); err != nil {
			t.Fatal(err)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			got, err := tx.Item("fact")
			if err != nil || got.Residency != domain.ResidencyResident || got.Version != 3 {
				t.Fatalf("unarchived item: %+v %v", got, err)
			}
			if !slices.EqualFunc(got.Parts, it.Parts, func(a, b domain.ContentPart) bool { return a == b }) || got.ContentHash != it.ContentHash {
				t.Fatalf("content did not round-trip: %+v", got)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
