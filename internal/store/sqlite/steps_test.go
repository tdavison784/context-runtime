package sqlite

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// committedSteps pins every Go migration step (F5: DUR-1.8, SPEC-1.9,
// SPEC-2.5): its identity, which is part of the migration's stored
// checksum, the function the registry runs, and the SHA-256 of the file
// holding that function's frozen code. A committed step is never edited; a
// changed transform is a new migration.
var committedSteps = map[int]struct{ id, fn, file, sum string }{
	11: {"0011/item-sources/reference-locator-v1", "backfillItemSourcesV1", "steps_0011.go", "e99a25b0aa67a7d3d2388fb739fe0d941430f38a09a61e89726008d23ab1c409"}, 26: {"0026/obligations/reconcile-matcher-satisfaction-v1", "reconcileMatcherSatisfactionV1", "steps_0026.go", "15dbb840e8117e40ef37234ff68500595f61c017bc35c62088ee154745eaec75"},
}

const pkgPath = "github.com/tdavison784/context-runtime/internal/store/sqlite"

func TestCommittedStepsUnchanged(t *testing.T) {
	if len(migrationSteps) != len(committedSteps) {
		t.Fatalf("migration steps = %d, want the %d pinned in committedSteps", len(migrationSteps), len(committedSteps))
	}
	for n, want := range committedSteps {
		step, ok := migrationSteps[n]
		if !ok || step.id != want.id {
			t.Errorf("step %d id = %q, want %q", n, step.id, want.id)
		}
		// The registry runs exactly the frozen function (SPEC-2.5), not a
		// wrapper that could reach live code.
		if got := runtime.FuncForPC(reflect.ValueOf(step.run).Pointer()).Name(); got != pkgPath+"."+want.fn {
			t.Errorf("step %d runs %s, want %s.%s", n, got, pkgPath, want.fn)
		}
		b, err := os.ReadFile(want.file)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != want.sum {
			t.Errorf("%s checksum = %s, want %q: committed migration steps are never edited", want.file, got, want.sum)
		}
		// The frozen code does not depend on live domain rules.
		if strings.Contains(string(b), "domain.LocatorKey") || strings.Contains(string(b), "domain.LocatorRuleVersion") {
			t.Errorf("%s calls live domain code", want.file)
		}
	}
}

// TestMigrationChecksumCoversStep checks that a step's identity is part of
// its migration's stored checksum, so a database migrated by one step
// version refuses a binary whose step differs.
func TestMigrationChecksumCoversStep(t *testing.T) {
	sql := []byte("SELECT 1;")
	if migrationChecksum(sql, 0) == migrationChecksum(sql, 11) {
		t.Fatal("a Go step does not change its migration's checksum")
	}
	if migrationChecksum(sql, 0) != hex.EncodeToString(func() []byte { s := sha256.Sum256(sql); return s[:] }()) {
		t.Fatal("a migration without a step changed checksum form")
	}
}

// TestFrozenLocatorKeyMatchesLiveRuleV1 guards the frozen 0011 transform
// while the live rule is still v1: backfilled keys equal the keys InsertItem
// writes today.
func TestFrozenLocatorKeyMatchesLiveRuleV1(t *testing.T) {
	if domain.LocatorRuleVersion != locatorRuleV1 {
		t.Skip("live rule moved past v1; 0011 stays frozen at v1")
	}
	for _, c := range []struct {
		kind domain.SourceKind
		loc  string
	}{
		{domain.SourcePath, "docs/a.md"}, {domain.SourcePath, "./docs//a.md"}, {domain.SourcePath, "a/../b"},
		{domain.SourcePath, "../x"}, {domain.SourcePath, "/abs"}, {domain.SourcePath, "a b"}, {domain.SourcePath, `a\b`},
		{domain.SourceURL, "https://x/y"}, {domain.SourceURL, "nope"}, {domain.SourceTool, "t"}, {domain.SourcePath, ""},
	} {
		gk, gok := locatorKeyV1(string(c.kind), c.loc)
		wk, wok := domain.LocatorKey(c.kind, c.loc)
		if gk != wk || gok != wok {
			t.Errorf("(%s, %q): frozen %q %v, live %q %v", c.kind, c.loc, gk, gok, wk, wok)
		}
	}
}
