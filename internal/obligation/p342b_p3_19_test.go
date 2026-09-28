package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// TestP3_19_CurrentContentClaimsMatchAuthoritativeState: the CURRENT half of
// P3-19's file modes, on the assertion path (SPEC-1.11's cited test covers
// only FIXED_HASH). A CURRENT_CONTENT obligation is satisfied only by a
// claim naming the path's AUTHORITATIVE CURRENT content and revision — not
// the old content at the new revision, not the new revision with other
// bytes, not another file's current content, and not a fixed-content
// claim, which is meaningful only against a FIXED_HASH target. The proof it
// installs is bound to that current state: the next change to the path
// invalidates it, new authoritative content re-opens it, and the very same
// edit leaves a FIXED_HASH obligation's snapshot claim valid — the two
// modes differ exactly as published.
func TestP3_19_CurrentContentClaimsMatchAuthoritativeState(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		ref := f.fileObligation(t, "11") // CURRENT_CONTENT file_read on docs/a.md

		claim := func(kind domain.ProofDependencyKind, path, content string, rev uint64) error {
			t.Helper()
			o := f.status(t, ref)
			l := domain.ResourceLocator{ResourceID: "repo1", BaseDir: ".", Path: path}
			in := intent(ref, o.Revision, domain.ObligationSatisfied)
			in.AssertionMode = domain.AssertionResourceBound
			in.Resources = []domain.ResourceClaim{{Kind: kind, ResourceID: "repo1", ResourceRevision: rev, Fingerprint: hashOf(content), Locator: &l}}
			_, err := f.s.transition(t, f.st, f.system, in)
			return err
		}
		refused := func(what string, err error) {
			t.Helper()
			if !errors.Is(err, domain.ErrUnknownApplicability) {
				t.Fatalf("%s: %v, want ErrUnknownApplicability", what, err)
			}
		}

		// Fail closed: with no reported content for the path, no claim applies.
		before := f.r.auth
		refused("claim before any path state", claim(domain.DependencyCurrentPath, "docs/a.md", "H1", before))

		// Both files have current authoritative content at revision rev1.
		f.resourceReport(t, "W1", true, false, nil,
			domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")},
			domain.ResourcePathContent{Path: "docs/b.md", ContentHash: hashOf("B1")})
		rev1 := f.r.auth
		if o := f.status(t, ref); o.Status != domain.ObligationUnresolved || o.Revision != 1 {
			t.Fatalf("setup: refused probes changed the obligation: %+v", o)
		}

		// Wrong content at the current revision.
		refused("other bytes at the current revision", claim(domain.DependencyCurrentPath, "docs/a.md", "OTHER", rev1))
		// The once-current bytes at the superseded revision.
		refused("current bytes at a stale revision", claim(domain.DependencyCurrentPath, "docs/a.md", "H1", before))
		// Another file's current content, at its current revision: a valid
		// claim of the wrong file never covers the target.
		refused("another file's current content", claim(domain.DependencyCurrentPath, "docs/b.md", "B1", rev1))
		// A fixed-content claim — even of exactly the current bytes — is only
		// meaningful against a FIXED_HASH target (SPEC-1.11); on a
		// CURRENT_CONTENT obligation it would smuggle an attestation in under
		// a RESOURCE_BOUND label.
		refused("fixed-content claim on a CURRENT_CONTENT target", claim(domain.DependencyFixedContent, "docs/a.md", "H1", 0))
		if o := f.status(t, ref); o.Status != domain.ObligationUnresolved || o.Revision != 1 {
			t.Fatalf("refused claims changed the obligation: %+v", o)
		}

		// The one claim that applies: this file's current bytes at the
		// authoritative revision. It installs a proof.
		if err := claim(domain.DependencyCurrentPath, "docs/a.md", "H1", rev1); err != nil {
			t.Fatalf("current-content claim: %v", err)
		}
		sat := f.status(t, ref)
		if sat.Status != domain.ObligationSatisfied || sat.CurrentProofID == "" {
			t.Fatalf("current-content claim installed no proof: %+v", sat)
		}

		// The proof is bound to the current state: the path's next change
		// invalidates it, and new authoritative content re-opens the target.
		f.resourceReport(t, "W2", false, false, []string{"docs/a.md"})
		f.wantInvalidated(t, ref, "repo1", "path edit kept a current-content proof")
		refused("old bytes after the edit", claim(domain.DependencyCurrentPath, "docs/a.md", "H1", rev1))
		f.resourceReport(t, "W2b", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H2")})
		if err := claim(domain.DependencyCurrentPath, "docs/a.md", "H2", f.r.auth); err != nil {
			t.Fatalf("claim of the new authoritative content: %v", err)
		}
		if o := f.status(t, ref); o.Status != domain.ObligationSatisfied || o.CurrentProofID == sat.CurrentProofID {
			t.Fatalf("re-opened target did not re-prove on new content: %+v", o)
		}

		// The mode contrast: a FIXED_HASH obligation over the same path is
		// satisfied by its required snapshot even now, with the path edited
		// and holding different current bytes.
		fixed := fileTarget("repo1", "docs/a.md", domain.FileFixedHash, hashOf("REQ"))
		in := domain.DeclareObligationIntent{RequestID: "d19-fix", SourceItemID: "pu", DeclarationSlot: "12", Description: "read it",
			ExpectedSourceVersion: 1, Target: &fixed, Matcher: &FileReadV1}
		if _, err := f.s.declare(t, f.st, f.harness, in); err != nil {
			t.Fatal(err)
		}
		key, _ := f.item(t, "pu").CurrentKey()
		n, _ := harnessSlot("12")
		fixedRef := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(key, n), Version: 1}
		fixedClaim := func(kind domain.ProofDependencyKind, content string, rev uint64) error {
			t.Helper()
			o := f.status(t, fixedRef)
			l := domain.ResourceLocator{ResourceID: "repo1", BaseDir: ".", Path: "docs/a.md"}
			c := domain.ResourceClaim{Kind: kind, ResourceID: "repo1", ResourceRevision: rev, Fingerprint: hashOf(content), Locator: &l}
			ci := intent(fixedRef, o.Revision, domain.ObligationSatisfied)
			ci.AssertionMode = domain.AssertionResourceBound
			ci.Resources = []domain.ResourceClaim{c}
			_, err := f.s.transition(t, f.st, f.system, ci)
			return err
		}
		refused("current-content claim naming other-than-required bytes on FIXED_HASH", fixedClaim(domain.DependencyCurrentPath, "H2", f.r.auth))
		if err := fixedClaim(domain.DependencyFixedContent, "REQ", 0); err != nil {
			t.Fatalf("fixed-content claim of the required snapshot after the edit: %v", err)
		}
		if o := f.status(t, fixedRef); o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
			t.Fatalf("FIXED_HASH snapshot claim after the edit: %+v", o)
		}
	})
}
