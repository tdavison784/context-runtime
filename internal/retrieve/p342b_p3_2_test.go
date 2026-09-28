package retrieve

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
)

// TestP3_2_ExpiredLeaseStillReplaysItsReceipt closes the MISSING half of
// "expired retrieval receipt replay" (ADR 8 :1047).
// TestRetrievalReceiptReplayPrecedesCurrentState proves the replay contract
// only over a stubbed reader whose observed expiry is LIVE — no lease is ever
// built, let alone expired. Here the real path runs on both stores: a
// Rehydrate admits and leases, the conversation's completed inferences then
// exhaust the lease's call allowance (the one liveness derivation, P3-29/31),
// and the expired state is proven real by a NEW request minting a NEW lease.
// The expired lease's committed request still replays through the stored
// receipt — same result, same old lease, no new lease — and the same request
// with changed canonical arguments is an ErrEventIDConflict, not a fresh
// retrieval.
func TestP3_2_ExpiredLeaseStillReplaysItsReceipt(t *testing.T) {
	for name, open := range map[string]func(t *testing.T) store.Store{
		"memory": func(*testing.T) store.Store { return memory.New() },
		"sqlite": func(t *testing.T) store.Store {
			s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "p342b-p3-2.db"))
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := open(t)
			defer s.Close()
			p, conv := seedLeaseStore(t, s)
			svc := New(s)
			first, err := svc.Rehydrate(ctx, p, harnessIntent(p, "request-1"), leasePolicy(), false)
			if err != nil || first.LeaseID == "" {
				t.Fatalf("first rehydrate: %+v %v", first, err)
			}
			if n := len(storedLeases(t, s, p)); n != 1 {
				t.Fatalf("leases after first admit: %d", n)
			}

			// Expire the lease: completed inferences reach the allowance bound.
			if err := s.Update(ctx, "s", func(tx store.Tx) error {
				next := conv
				next.LogicalCalls = storeIssuedIndex + leasePolicy().DefaultLeaseCalls
				next.Revision++
				_, err := tx.PutConversation(next, conv.Revision)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// The expiry is real: a new request cannot use the dead lease.
			fresh, err := svc.Rehydrate(ctx, p, harnessIntent(p, "request-3"), leasePolicy(), false)
			if err != nil || fresh.LeaseID == first.LeaseID || len(storedLeases(t, s, p)) != 2 {
				t.Fatalf("expired lease reused: %+v %v", fresh, err)
			}

			// The committed request still replays: same result, same old lease,
			// no third lease minted.
			replayed, err := svc.Rehydrate(ctx, p, harnessIntent(p, "request-1"), leasePolicy(), false)
			if err != nil || replayed.ID != first.ID || replayed.LeaseID != first.LeaseID {
				t.Fatalf("replay of the expired lease's request = %+v, %v", replayed, err)
			}
			if n := len(storedLeases(t, s, p)); n != 2 {
				t.Fatalf("replay minted a lease: %d", n)
			}

			// The same request with changed canonical arguments conflicts; it is
			// never a fresh retrieval under an owned receipt.
			changed := harnessIntent(p, "request-1")
			changed.Rehydrate.ItemID = "different"
			if _, err := svc.Rehydrate(ctx, p, changed, leasePolicy(), false); !errors.Is(err, domain.ErrEventIDConflict) {
				t.Fatalf("changed replay: %v, want ErrEventIDConflict", err)
			}
		})
	}
}
