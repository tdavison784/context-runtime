package domain

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// registeredCanonicalDomains is the golden canonical-encoder domain registry
// (ADR 4). Every domain separates one identity or hash family; reusing or
// silently changing one would let distinct values collide. Registering a
// new domain means adding it here and to ADR 4's registry, in one commit.
var registeredCanonicalDomains = []string{
	"context-runtime/call-id/v2",
	"context-runtime/call-lifecycle-id/v1",
	"context-runtime/call-outcome/v2",
	"context-runtime/call-proposal/v1",
	"context-runtime/collect-audit/v1",
	"context-runtime/collect-receipt/v1",
	"context-runtime/content/v1",
	"context-runtime/conversation/v1",
	"context-runtime/coverage-member/v1",
	"context-runtime/coverage/v1",
	"context-runtime/creation-declaration/v1",
	"context-runtime/current-key/v2",
	"context-runtime/event-occurrence/v1",
	"context-runtime/gc-request/v1",
	"context-runtime/gc-result/v1",
	"context-runtime/gc-trigger/v1",
	"context-runtime/gc-trigger/v2",
	"context-runtime/grant-revocation-audit/v1",
	"context-runtime/grant-target/v1",
	"context-runtime/graph/creation-declaration-id/v1",
	"context-runtime/graph/derived-coverage-id/v1",
	"context-runtime/graph/derived-relationship-id/v2",
	"context-runtime/graph/lifecycle-event-id/v2",
	"context-runtime/graph/obligation-audit-id/v1",
	"context-runtime/graph/relationship-id/v1",
	"context-runtime/graph/snapshot-declaration-id/v1",
	"context-runtime/ingest-payload/v2",
	"context-runtime/ingest-payload/v3",
	"context-runtime/ingest/outcome-event-id/v1",
	"context-runtime/ingest/owner-registration-id/v1",
	"context-runtime/ingest/task-audit-id/v1",
	"context-runtime/item-id/v1",
	"context-runtime/lifecycle-audit/v1",
	"context-runtime/lifecycle-change/v1",
	"context-runtime/lifecycle-replacement-event/v1",
	"context-runtime/lifecycle-replacement-item/v1",
	"context-runtime/logical-membership-id/v1",
	"context-runtime/mutation-receipt-id/v1",
	"context-runtime/mutation-request/v3",
	"context-runtime/obligation-id/v1",
	"context-runtime/obligation-target/v1",
	"context-runtime/observation-subject/v1",
	"context-runtime/operation-request-binding/v1",
	"context-runtime/operation-request-binding/v2",
	"context-runtime/operation-request-id/v1",
	"context-runtime/operation-request-id/v2",
	"context-runtime/operation-request-id/v3",
	"context-runtime/proof-id/v1",
	"context-runtime/resource-locator/v1",
	"context-runtime/retrieval-record/v1",
	"context-runtime/semantic-arguments/v1",
	"context-runtime/snapshot-declaration/v1",
	"context-runtime/task-completion-audit/v1",
	"context-runtime/tool-invocation/v1",
	"context-runtime/tools/id/v1",
	"context-runtime/turn-id/v1",
	"context-runtime/w4/obligation.declare/v1",
	"context-runtime/w4/obligation.materialization/v1",
	"context-runtime/w4/obligation.reevaluate/v1",
	"context-runtime/w4/obligation.transition/v1",
	"context-runtime/w4/observation.report/v1",
	"context-runtime/w4/observation.run/v1",
	"context-runtime/w4/record-id/v1",
	"context-runtime/w4/resource.register/v1",
	"context-runtime/w4/resource.report/v1",
	"context-runtime/w4/resource.resync/v1",
	"context-runtime/w4/workspace.bind/v1",
}

var canonicalDomainPattern = regexp.MustCompile(`^context-runtime/[a-z0-9.-]+(/[a-z0-9.-]+)*/v[1-9][0-9]*$`)

func TestCanonicalDomainRegistryIsSortedUniqueAndVersioned(t *testing.T) {
	if !slices.IsSorted(registeredCanonicalDomains) || len(slices.Compact(slices.Clone(registeredCanonicalDomains))) != len(registeredCanonicalDomains) {
		t.Fatal("registry must be sorted and unique: a repeated domain is a collision")
	}
	for _, d := range registeredCanonicalDomains {
		if !canonicalDomainPattern.MatchString(d) {
			t.Errorf("%q is not a versioned context-runtime domain", d)
		}
	}
}

// Computed domains cannot be found by the source scan, so they are checked
// against the registry directly.
func TestComputedCanonicalDomainsAreRegistered(t *testing.T) {
	for _, f := range []MutationFamily{
		MutationObligationDeclare, MutationObligationTransition, MutationObligationMaterialization, MutationObligationReevaluate,
		MutationWorkspaceBind, MutationResourceRegister, MutationResourceReport, MutationResourceResync,
		MutationObservationRun, MutationObservationReport,
		MutationLifecycle, MutationObligation, MutationGrantFamily, MutationResource, MutationTool, MutationRetrieval, MutationCollection, MutationMembership,
	} {
		if d := f.HashDomain(); !slices.Contains(registeredCanonicalDomains, d) {
			t.Errorf("family %s hash domain %q is unregistered", f, d)
		}
	}
}

// TestSourceCanonicalDomainsAreRegistered fails when production code
// introduces a literal domain without registering it.
func TestSourceCanonicalDomainsAreRegistered(t *testing.T) {
	literal := regexp.MustCompile(`"(context-runtime/[^"]*/v[0-9]+)"`)
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range literal.FindAllStringSubmatch(string(b), -1) {
			if !slices.Contains(registeredCanonicalDomains, m[1]) {
				t.Errorf("%s uses unregistered canonical domain %q", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestADR4ListsEveryRegisteredCanonicalDomain(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "adr", "0004-ids-idempotency-and-hashes.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range registeredCanonicalDomains {
		if !strings.Contains(string(b), "`"+d+"`") {
			t.Errorf("ADR 4 registry omits %q", d)
		}
	}
}
