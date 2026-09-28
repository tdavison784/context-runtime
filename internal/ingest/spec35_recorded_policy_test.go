package ingest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/lifecycle"
)

// TestLifecycleExecutionUsesRecordedPolicy_SPEC35 (SPEC-3.5, SPEC-2.11,
// P3-39/40): every lifecycle execution ingest performs for an event (typed
// REPLACE here, and a parsed Resolve) runs under that event's recorded
// policy. An executor frozen with a different policy is a configuration
// error: the event is refused with nothing written, instead of silently
// losing an enabled SUPERSESSION trigger. With matching policies the typed
// replacement produces its trigger.
func TestLifecycleExecutionUsesRecordedPolicy_SPEC35(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		on := testPolicy()
		on.GCTriggers = domain.DefaultGCTriggers()
		off := testPolicy()
		off.GCTriggers = []domain.GCTrigger{domain.GCManual, domain.GCTaskCompletion}
		executor := func(p domain.Phase3Policy) *lifecycle.Service {
			svc, err := lifecycle.New(f.s, p)
			if err != nil {
				t.Fatal(err)
			}
			return svc
		}
		sys := principal(domain.AuthoritySystem)
		f.in.Semantic, f.in.Lifecycle = &on, executor(on)
		r := f.mustIngest(sys, sysEvent("v1", "## Pinned\n- [p] one\n## Goal [g]\nShip.\n"))
		p := mustDirective(t, r, "p")
		replace := domain.Event{EventID: "replace", Kind: domain.EventSystem, Operations: []domain.SemanticOperation{{Kind: domain.OperationReplace,
			Replace: &domain.ReplaceDirectiveIntent{ItemMutationIntent: domain.ItemMutationIntent{ItemID: p.ID, ExpectedVersion: p.Version}, Parts: p.Parts}}}}

		// The executor's policy disables SUPERSESSION; the recorded one enables it.
		f.in.Lifecycle = executor(off)
		f.requireAtomic(domain.ErrUnsupportedSchema, func() error {
			_, err := f.ingest(sys, replace)
			return err
		})
		f.requireAtomic(domain.ErrUnsupportedSchema, func() error {
			_, err := f.ingest(sys, sysEvent("resolve", "## Resolve [g]\n"))
			return err
		})

		f.in.Lifecycle = executor(on)
		f.mustIngest(sys, replace)
		if n := len(f.gcRequests()[domain.GCSupersession]); n != 1 {
			t.Fatalf("typed replacement under the recorded policy enqueued %d SUPERSESSION requests, want 1", n)
		}
	})
}
