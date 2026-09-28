package retrieve

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// seedOversizeSource mirrors seedLeaseStore with a source whose raw content
// dwarfs the small delivery limit the test passes.
func seedOversizeSource(t *testing.T, s store.Store) domain.Principal {
	t.Helper()
	p := storetest.NewPrincipal("s", domain.AuthorityHarness)
	conv := storetest.NewConversation("s", domain.ConversationIDFor(p.TaskID, p.AgentID))
	conv.LogicalCalls = storeIssuedIndex
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		if _, err := tx.PutTask(storetest.NewTask("s", p.TaskID), 0, domain.LifecycleEvent{ID: "task-open", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: p.TaskID, Action: "open", Actor: p}); err != nil {
			return err
		}
		if _, err := tx.PutConversation(conv, 0); err != nil {
			return err
		}
		source := storetest.NewItem("s", "source", tx.NextSeq(), "historical content")
		source.Parts = append(source.Parts, domain.ContentPart{Type: domain.PartText, MediaType: "text/plain", Text: strings.Repeat("x", 4096)})
		source.ContentHash = domain.ContentHash(source.Parts)
		source.SemanticBytes = domain.SemanticBytes(source.Parts)
		source.Residency = domain.ResidencyArchived
		return tx.InsertItem(source)
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestP3_30_OversizeWithAndWithoutRegisteredProjection closes the P3-42
// table row "oversize with/without registered projection": without the
// registered stub policy an oversize source is refused outright — the closed
// TOO_LARGE denial names no source identity and persists no lease or result —
// while with the registered retrieval-stub/v1 policy the same source
// delivers a stub projection that omits exactly the raw content byte count,
// names the context_get route, and never cuts the stored raw parts.
func TestP3_30_OversizeWithAndWithoutRegisteredProjection(t *testing.T) {
	for name, open := range map[string]func(t *testing.T) store.Store{
		"memory": func(*testing.T) store.Store { return memory.New() },
		"sqlite": func(t *testing.T) store.Store {
			s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "p330.db"))
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			defer s.Close()
			p := seedOversizeSource(t, s)
			pol := leasePolicy()
			pol.MaxToolResultBytes = 512 // above the stub notice, far below full delivery
			svc := New(s)
			const raw = uint64(len("historical content") + 4096)

			// Without the registered projection the oversize result is
			// refused with the closed code.
			_, err := svc.Rehydrate(context.Background(), p, harnessIntent(p, "big1"), pol, false)
			if err == nil || !strings.Contains(err.Error(), ErrResultTooLarge.Error()) {
				t.Fatalf("oversize without stub: %v", err)
			}
			if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
				sem, err := store.ReadSemantic(tx)
				if err != nil {
					return err
				}
				events, err := sem.RetrievalEventsByRequest(p, "big1", store.Page{Limit: 4})
				if err != nil {
					return err
				}
				if len(events.Records) != 1 || events.Records[0].ErrorCode != domain.ToolErrorTooLarge || events.Records[0].Source != nil || events.Records[0].ResultID != "" {
					t.Fatalf("denial event: %+v", events.Records)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if leases := storedLeases(t, s, p); len(leases) != 0 {
				t.Fatalf("refused oversize persisted a lease: %+v", leases)
			}

			// With the registered retrieval-stub/v1 policy the same source
			// delivers a stub.
			out, err := svc.Rehydrate(context.Background(), p, harnessIntent(p, "big2"), pol, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
				sem, err := store.ReadSemantic(tx)
				if err != nil {
					return err
				}
				projection, err := sem.Projection(out.ProjectionID)
				if err != nil || projection.DeliveryPolicyVersion != stubDeliveryVersion || projection.OmittedBytes != raw {
					t.Fatalf("stub projection: %+v %v", projection, err)
				}
				item, err := tx.Item(projection.ItemID)
				if err != nil {
					return err
				}
				text := item.Parts[0].Text
				if !strings.Contains(text, "context_get(source)") || !strings.Contains(text, stubDeliveryVersion) {
					t.Fatalf("stub notice lost its route: %q", text)
				}
				// The stored raw source is never cut.
				source, err := tx.Item("source")
				if err != nil || len(source.Parts) != 2 || source.Parts[1].Text != strings.Repeat("x", 4096) || source.Version != 1 {
					t.Fatalf("raw source altered: %+v %v", source, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
