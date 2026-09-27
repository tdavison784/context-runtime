package ingest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
)

// testdata/phase2/reserved.db was written by the Phase 2 binary (main,
// b869cb1) from reservedLegacyEvents, which Phase 2 accepted as plain USER
// events; reserved.golden.json holds each receipt as that binary returned
// it. Only copies are ever opened.

// reservedLegacyEvents are plain events whose EventIDs fall in namespaces
// reserved later (outcome-, req_, gc_), plus an unreserved control.
func reservedLegacyEvents() []domain.Event {
	return []domain.Event{
		userEvent("plain-legacy", "hello", false),
		userEvent("outcome-legacy", "an outcome-named note", false),
		userEvent("req_legacy", "## Goal [lg]\nLegacy goal.\n", true),
		userEvent("gc_legacy", "a gc-named note", false),
	}
}

// TestReservedNamespaceReceiptsReplay_DUR28 (DUR-2.8, H5, FR-ING-006): an
// exact retry of a recorded plain event replays its receipt before any
// reserved-namespace check, so Phase 2 history under an EventID that a later
// phase reserved still replays verbatim, allocating nothing. A changed
// request under such an ID is a conflict, and a new event can still never
// claim a reserved namespace.
func TestReservedNamespaceReceiptsReplay_DUR28(t *testing.T) {
	var golden map[string]domain.IngestReceipt
	b, err := os.ReadFile(filepath.Join(phase2Dir, "reserved.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "reserved.db")
	copyFile(t, filepath.Join(phase2Dir, "reserved.db"), path)
	s, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("open frozen reserved-namespace database: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	f := newFixture(t, s)
	user := principal(domain.AuthorityUser)
	seq := f.lastSeq()
	for _, e := range reservedLegacyEvents() {
		want, ok := golden[e.EventID]
		if !ok {
			t.Fatalf("golden has no %s", e.EventID)
		}
		got, err := f.ingest(user, e)
		if err != nil || !reflect.DeepEqual(normReceipt(got), normReceipt(want)) {
			t.Errorf("%s: retry = %v (equal %v)", e.EventID, err, err == nil && reflect.DeepEqual(normReceipt(got), normReceipt(want)))
		}
		changed := e
		changed.Spans = []domain.Span{textSpan(domain.AuthorityUser, false, "different")}
		if _, err := f.ingest(user, changed); !errors.Is(err, domain.ErrEventIDConflict) {
			t.Errorf("%s: changed request = %v, want conflict", e.EventID, err)
		}
	}
	if f.lastSeq() != seq {
		t.Fatalf("retries allocated sequences: %d -> %d", seq, f.lastSeq())
	}
	for _, id := range []string{"outcome-new", "req_new", "gc_new"} {
		f.requireAtomic(domain.ErrInvalidRecord, func() error {
			_, err := f.ingest(user, userEvent(id, "new", false))
			return err
		})
	}
}
