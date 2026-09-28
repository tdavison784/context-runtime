package ingest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// P3-4 (ADR 8 :1055/:1056/:1061): creation declarations govern ordinary
// restatement — the comparison never consults residency, rejected attributes
// and nonsemantic spellings are ignored while accepted attributes are not,
// and a duplicate is a noncurrent audit occurrence that creates no
// obligation and never acts as an active requirement.

// TestP3_4_RestatementAfterArchiveStaysArchived closes the MISSING Archive
// case of "identical restatement after Resolve/Unpin/Archive" (ADR 8 :1055):
// Resolve and Unpin are covered by TestP336_ResolvedRestatementStaysResolved
// and TestP336_UnpinnedRestatementStaysUnpinned. Archiving a goal changes
// only residency, so a verbatim restatement is still a duplicate — never a
// replacement — and the archived goal stays current, ARCHIVED and untouched.
func TestP3_4_RestatementAfterArchiveStaysArchived(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		first := f.mustIngest(sys, sysEvent("orig", "## Goal [g]\nShip.\n"))
		g := mustDirective(t, first, "g")
		if _, err := f.lifecycleService().ArchiveStandalone(ctx, sys, domain.ArchiveIntent{RequestID: "arch", ItemID: g.ID, ExpectedVersion: f.item(g.ID).Version}); err != nil {
			t.Fatalf("archive: %v", err)
		}
		archived := f.item(g.ID)
		if archived.Residency != domain.ResidencyArchived || !f.isCurrent(g.ID) {
			t.Fatalf("setup: residency %s, current %v", archived.Residency, f.isCurrent(g.ID))
		}
		again := f.mustIngest(sys, sysEvent("again", "## Goal [g]\nShip.\n"))
		dup := mustDirective(t, again, "g")
		if len(again.Replacements) != 0 || len(semanticDups(again)) != 1 {
			t.Fatalf("restatement after archive: replacements %+v duplicates %+v", again.Replacements, semanticDups(again))
		}
		after := f.item(g.ID)
		if !f.isCurrent(g.ID) || f.isCurrent(dup.ID) {
			t.Fatalf("currentness: archived original %v, duplicate %v", f.isCurrent(g.ID), f.isCurrent(dup.ID))
		}
		if after.Residency != domain.ResidencyArchived || after.Version != archived.Version {
			t.Fatalf("restatement changed the archived goal: residency %s, version %d -> %d", after.Residency, archived.Version, after.Version)
		}
	})
}

// TestP3_4_AcceptedVersusIgnoredAttributeRestatements closes the P3-42 row
// "changed accepted versus ignored attributes" (ADR 8 :1056). Ignored changes
// — restating the default kind explicitly, and an unknown attribute that is
// diagnosed and dropped — leave creation identity unchanged, so the
// restatement is a duplicate. An accepted change — adding obligation=<claim>
// — changes the declaration, so the same text becomes an authorized
// replacement that starts the obligation.
func TestP3_4_AcceptedVersusIgnoredAttributeRestatements(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		r1 := f.mustIngest(sys, sysEvent("b1", "## Pinned\n- [dep] Use dependency v2.\n"))
		p1 := mustDirective(t, r1, "dep")

		// Ignored change: kind=constraint restates the Pinned default
		// (FR-DIR-003), a nonsemantic spelling difference.
		r2 := f.mustIngest(sys, sysEvent("b2", "## Pinned\n- [dep] {kind=constraint} Use dependency v2.\n"))
		spelled := mustDirective(t, r2, "dep")
		if d := semanticDups(r2); len(d) != 1 || d[0] != (domain.IngestLink{ItemID: spelled.ID, TargetID: p1.ID}) || len(r2.Replacements) != 0 {
			t.Errorf("explicit default: dups %v, replacements %v; want one duplicate, no replacement", d, r2.Replacements)
		}
		if spelled.Kind != domain.KindConstraint || f.isCurrent(spelled.ID) || !f.isCurrent(p1.ID) {
			t.Errorf("explicit default: kind %s, spelled current %v, bare current %v", spelled.Kind, f.isCurrent(spelled.ID), f.isCurrent(p1.ID))
		}

		// Ignored change: an unknown attribute is diagnosed and dropped
		// (FR-DIR-006), never folded into identity.
		r3 := f.mustIngest(sys, sysEvent("b3", "## Pinned\n- [dep] {bogus=1} Use dependency v2.\n"))
		unknown := mustDirective(t, r3, "dep")
		if d := semanticDups(r3); len(d) != 1 || d[0] != (domain.IngestLink{ItemID: unknown.ID, TargetID: p1.ID}) || len(r3.Replacements) != 0 {
			t.Errorf("unknown attribute: dups %v, replacements %v; want one duplicate, no replacement", d, r3.Replacements)
		}
		if !hasDiag(r3, domain.ErrMalformedDirective, domain.ReasonUnknownAttribute) {
			t.Errorf("unknown attribute was not diagnosed: %+v", r3.Diagnostics)
		}
		if f.isCurrent(unknown.ID) || !f.isCurrent(p1.ID) {
			t.Errorf("unknown attribute: duplicate current %v, canonical current %v", f.isCurrent(unknown.ID), f.isCurrent(p1.ID))
		}

		// Accepted change: obligation=<claim> is the one attribute the row
		// does not capture, so adding it is a changed declaration and the
		// identical text becomes a replacement that owns a new obligation.
		r4 := f.mustIngest(sys, sysEvent("b4", "## Pinned\n- [dep] {obligation=tests_pass} Use dependency v2.\n"))
		p2 := mustDirective(t, r4, "dep")
		if len(r4.Replacements) != 1 || r4.Replacements[0].TargetID != p1.ID || len(semanticDups(r4)) != 0 {
			t.Fatalf("accepted change: replacements %+v, dups %v; want one replacement of the bare pin", r4.Replacements, semanticDups(r4))
		}
		if !f.isCurrent(p2.ID) || f.isCurrent(p1.ID) {
			t.Fatalf("accepted change: replacement current %v, replaced current %v", f.isCurrent(p2.ID), f.isCurrent(p1.ID))
		}
		f.view(func(tx store.ReadTx) error {
			bare, err := tx.ObligationsBySource(p1.ID, 10)
			if err != nil {
				return err
			}
			if len(bare) != 0 {
				t.Errorf("bare spelling created obligations: %+v", bare)
			}
			cur, err := tx.ObligationsBySource(p2.ID, 10)
			if err != nil {
				return err
			}
			if len(cur) != 1 || cur[0].Claim != "tests_pass" || !cur[0].Current {
				t.Errorf("accepted-change obligations = %+v, want one current tests_pass", cur)
			}
			return nil
		})
	})
}

// TestP3_4_DuplicateRawTextIsNotAnActiveRequirement closes the P3-42 row
// "duplicate raw text not an active requirement" (ADR 8 :1061).
// TestNonDirectiveDuplicates_D10 covers non-directive detection only. A
// verbatim restatement of a requirement-bearing pin is a noncurrent audit
// occurrence: it holds no obligation and creates none, while the canonical
// keeps exactly its one current obligation. A genuinely changed text stays
// an active requirement (the replacement control).
func TestP3_4_DuplicateRawTextIsNotAnActiveRequirement(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		text := "## Pinned\n- [req] {obligation=tests_pass} All tests must pass.\n"
		r1 := f.mustIngest(sys, sysEvent("c1", text))
		p1 := mustDirective(t, r1, "req")
		r2 := f.mustIngest(sys, sysEvent("c2", text))
		dup := mustDirective(t, r2, "req")
		if d := semanticDups(r2); len(d) != 1 || d[0] != (domain.IngestLink{ItemID: dup.ID, TargetID: p1.ID}) || len(r2.Replacements) != 0 {
			t.Fatalf("verbatim restatement: dups %v, replacements %v", d, r2.Replacements)
		}
		if f.isCurrent(dup.ID) {
			t.Fatalf("duplicate raw text is current")
		}
		f.view(func(tx store.ReadTx) error {
			dupObs, err := tx.ObligationsBySource(dup.ID, 10)
			if err != nil {
				return err
			}
			if len(dupObs) != 0 {
				t.Errorf("duplicate raw text holds %d obligations, want none", len(dupObs))
			}
			cur, err := tx.ObligationsBySource(p1.ID, 10)
			if err != nil {
				return err
			}
			if len(cur) != 1 || !cur[0].Current || cur[0].Claim != "tests_pass" {
				t.Errorf("canonical obligations = %+v, want the one current tests_pass", cur)
			}
			all, err := tx.Obligations("T")
			if err != nil {
				return err
			}
			current := 0
			for _, o := range all {
				if o.Current {
					current++
				}
			}
			if current != 1 {
				t.Errorf("task holds %d current obligations after the duplicate, want 1", current)
			}
			return nil
		})
		// Replacement control: changed text is an active requirement again.
		r3 := f.mustIngest(sys, sysEvent("c3", "## Pinned\n- [req] {obligation=tests_pass} All tests must pass twice.\n"))
		p3 := mustDirective(t, r3, "req")
		if len(r3.Replacements) != 1 || r3.Replacements[0].TargetID != p1.ID || !f.isCurrent(p3.ID) {
			t.Fatalf("changed text: replacements %+v, current %v", r3.Replacements, f.isCurrent(p3.ID))
		}
	})
}
