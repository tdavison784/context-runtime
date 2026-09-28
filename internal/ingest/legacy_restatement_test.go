package ingest

import (
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestLegacyRestatementDedupsAfterUpgrade checks G5 (SPEC-1.3, FROZEN C-1,
// P3-4/41) on the frozen Phase 2 database: migration reconciles each
// pre-upgrade directive's creation declaration from its immutable receipt
// snapshot, so restating every current pre-upgrade directive verbatim is a
// duplicate. Nothing is replaced, re-pinned or unarchived, and no
// obligation is retired, rebound or versioned.
func TestLegacyRestatementDedupsAfterUpgrade(t *testing.T) {
	s := openPhase2Copy(t)
	if !hasSemantic(s) {
		t.Skip("GATE-PENDING: needs " + depW2)
	}
	f := newFixture(t, s)
	before := snapshotPhase2(t, s)
	user := principal(domain.AuthorityUser)
	for _, c := range []struct {
		p domain.Principal
		e domain.Event
	}{
		{principal(domain.AuthoritySystem), sysEvent("g5-sys", "## Goal [ship]\nShip the release.\n## Pinned\n- [suite] All tests must pass.\n- [reada] Read a.go\n- [deps] {obligation=tests_pass} Use dependency v3.\n")},
		{user, userEvent("g5-user", "## Pinned\n- [style] {obligation=file_read} Keep style.\n## Goal [ug]\nWrite docs.\n", true)},
		{user, userEvent("g5-amb-a", "## Pinned\n- [p] {scope=AGENT} private rule\n", true)},
		{phase2Principal(domain.AuthorityHarness, ""), harnessSpanEvent("g5-amb-h", "## Pinned\n- [p] task-wide rule\n", false)},
	} {
		r, err := f.ingest(c.p, c.e)
		if err != nil {
			t.Errorf("%s: identical restatement failed: %v", c.e.EventID, err)
			continue
		}
		if len(r.Replacements) != 0 {
			t.Errorf("%s: identical restatement replaced %+v", c.e.EventID, r.Replacements)
		}
		dups := map[string]string{}
		for _, l := range r.Duplicates {
			dups[l.ItemID] = l.TargetID
		}
		for _, it := range semantic(r) {
			if it.DirectiveID == "" {
				continue
			}
			prior, ok := dups[it.ID]
			if !ok {
				t.Errorf("%s: [%s] is not a duplicate", c.e.EventID, it.DirectiveID)
				continue
			}
			if !f.isCurrent(prior) {
				t.Errorf("%s: [%s] prior %s is no longer current", c.e.EventID, it.DirectiveID, prior)
			}
		}
	}
	after := snapshotPhase2(t, s)
	if !reflect.DeepEqual(after.Versions, before.Versions) || !reflect.DeepEqual(after.Transitions, before.Transitions) {
		t.Errorf("identical restatement changed obligations:\nbefore %+v\nafter  %+v", before.Versions, after.Versions)
	}
	f.view(func(tx store.ReadTx) error {
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		for _, it := range before.Items {
			if it.DirectiveID == "" {
				continue
			}
			d, err := sem.CreationDeclaration(it.ID)
			if err != nil {
				t.Errorf("pre-upgrade [%s] %s has no reconciled declaration: %v", it.DirectiveID, it.ID, err)
				continue
			}
			if err := d.Validate(); err != nil || d.ItemID != it.ID {
				t.Errorf("pre-upgrade [%s] declaration invalid: %+v (%v)", it.DirectiveID, d, err)
			}
		}
		return nil
	})
}
