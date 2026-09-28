package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// SPEC-5.6 / DUR-4.8 / SEC-4.4: RearmGCRequest binds SYSTEM to the request's
// task exactly as collection does, so a foreign-task SYSTEM caller is
// indistinguishable from an absent request: absent, pending and failed
// requests all report ErrNotFound with the same message, and the failed
// request is never re-armed by it.
func TestRearmBindsSYSTEMToTheRequestTask_SPEC56(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		seedEphemeral(t, db, 0, 0)
		pending := enqueueScratch(t, db, s)
		failed := enqueueWithID(t, db, s, domain.GCSupersession, "scratch-2")
		failRequest(t, db, failed)
		foreign := storetest.NewPrincipal("s", domain.AuthoritySystem)
		foreign.TaskID = "other-task"
		try := func(id string) error {
			return db.Update(ctx, "s", func(tx store.Tx) error {
				_, err := s.RearmGCRequest(tx, foreign, id)
				return err
			})
		}
		absent, pend, fail := try("gcq_absent"), try(pending), try(failed)
		for _, err := range []error{absent, pend, fail} {
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("foreign-task SYSTEM re-arm is not not-found: %v", err)
			}
		}
		if absent.Error() != pend.Error() || pend.Error() != fail.Error() {
			t.Fatalf("foreign-task SYSTEM re-arm discloses request state: absent=%v pending=%v failed=%v", absent, pend, fail)
		}
		if list := pendingGC(t, db); len(list) != 1 || list[0].ID != pending {
			t.Fatalf("foreign-task SYSTEM re-arm created a request: %+v", list)
		}
		// Positive control: the same-task SYSTEM principal still re-arms.
		var rearm string
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			var err error
			rearm, err = s.RearmGCRequest(tx, storetest.NewPrincipal("s", domain.AuthoritySystem), failed)
			return err
		}); err != nil || rearm == "" {
			t.Fatalf("same-task SYSTEM re-arm: %q %v", rearm, err)
		}
	})
}
