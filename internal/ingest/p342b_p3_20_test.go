package ingest

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	neturl "net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// P3-20: replay performs no filesystem or network reads. A locator names
// where content came from; the snapshot bytes came in the event's parts and
// live in the store. Replay answers from those stored bytes — never by
// re-reading the locator (FR-ING-007, "locators are never dereferenced").
// Two tests pin that: a package-boundary test proving the replay and
// ingest-replay code paths (internal/ingest, internal/obligation,
// internal/graph) cannot even reach a filesystem or network API, and a
// behavioural test whose locators are poisoned — a path that does not exist
// and a URL in the RFC 6761 .invalid TLD that can never resolve — proving
// ingest and replay still work byte-identically over the supplied snapshot.

// p3_20Packages are the packages the replay and ingest-replay paths live in.
var p3_20Packages = []string{".", "../obligation", "../graph"}

// p3_20Families are the stdlib import families that can read the outside
// world: the os family (files, env, processes), io/fs (filesystem
// interfaces), the net family (sockets, http), syscall, and embed (which
// bakes a readable fs.FS into the binary).
func p3_20InFamily(path string) bool {
	return path == "os" || strings.HasPrefix(path, "os/") ||
		path == "io/fs" || path == "syscall" || path == "embed" ||
		path == "net" || strings.HasPrefix(path, "net/")
}

// p3_20Allowed is the explicit stdlib allowlist: the only members of the
// reading families these packages may import, each with the reason it
// cannot read. Anything else in a family fails the test; an entry that is
// not in a family fails too, so the allowlist cannot rot into nonsense.
var p3_20Allowed = map[string]string{
	"net/url": "pure locator parsing; opens no sockets and touches no files",
}

// TestP3_20_ReplayPackagesImportNoFilesystemOrNetwork: every production
// file of the replay and ingest-replay packages imports no os/net/io/fs
// API beyond the allowlist above, and the packages' own replay entry
// points are still the ones being parsed (so a rename cannot make the walk
// vacuous).
func TestP3_20_ReplayPackagesImportNoFilesystemOrNetwork(t *testing.T) {
	funcs := map[string]bool{}
	for _, dir := range p3_20Packages {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: no Go sources found (%v) — package moved?", dir, err)
		}
		n := 0
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			n++
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, name, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range f.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok {
					funcs[fn.Name.Name] = true
				}
			}
			for _, imp := range f.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(path, ".") { // in-repo or third-party, not stdlib
					continue
				}
				if !p3_20InFamily(path) {
					continue
				}
				if why, ok := p3_20Allowed[path]; ok {
					t.Logf("%s: allowed %s (%s)", name, path, why)
					continue
				}
				t.Errorf("%s imports %s: the replay path must not read the filesystem or network", name, path)
			}
		}
		if n == 0 {
			t.Fatalf("%s: only test files found — boundary check is vacuous", dir)
		}
	}
	// The walk must have covered the code that actually replays: ingest's
	// receipt lookup and the obligation service's replaying reevaluation.
	for _, want := range []string{"lookupReceipt", "ReevaluateTx"} {
		if !funcs[want] {
			t.Errorf("replay entry point %s not found under the parsed packages — boundary check is vacuous", want)
		}
	}
	for path := range p3_20Allowed {
		if !p3_20InFamily(path) {
			t.Errorf("allowlist entry %s is not a member of the reading families", path)
		}
	}
}

// p3_20Poisoned is a locator pair that cannot be dereferenced: a path under
// a directory that does not exist anywhere, and a URL whose host is in the
// RFC 6761 .invalid TLD, which is reserved to be non-resolvable. Reading
// either one has to fail.
func TestP3_20_ReplayUsesStoredBytesNotTheLocator(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "p3-20-no-such-directory", "src.md")
	if _, err := os.Stat(absent); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("precondition: poisoned path stat = %v, want not-exist", err)
	}
	url := "https://replay-must-not-deref.p3-20.invalid/src.md"
	if u, err := neturl.Parse(url); err != nil || !strings.HasSuffix(u.Hostname(), ".invalid") {
		t.Fatalf("precondition: %s is not in the RFC 6761 reserved .invalid TLD (%v)", url, err)
	}

	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		text := "## Working\n- the snapshot bytes shipped with the event\n"
		hash := domain.HashBytes([]byte(text))
		event := func(id string, kind domain.SourceKind, locator string) domain.Event {
			e := userEvent(id, text, true)
			e.Spans[0].Source = &domain.SourceRef{Kind: kind, Locator: locator, ContentHash: hash}
			return e
		}

		// First ingest succeeds over each poisoned locator: the bytes came in
		// the parts, and no path was read, no host resolved.
		var firsts []domain.IngestReceipt
		for i, src := range []domain.SourceRef{
			{Kind: domain.SourcePath, Locator: absent},
			{Kind: domain.SourceURL, Locator: url},
		} {
			r := f.mustIngest(user, event("p320-"+string(rune('a'+i)), src.Kind, src.Locator))
			firsts = append(firsts, r)
		}
		// The locator is stored as data: the item records it verbatim with the
		// hash of the bytes actually ingested.
		for _, src := range []domain.SourceRef{
			{Kind: domain.SourcePath, Locator: absent},
			{Kind: domain.SourceURL, Locator: url},
		} {
			found := false
			for _, it := range f.items() {
				if it.Source != nil && it.Source.Locator == src.Locator {
					found = true
					if it.Source.Kind != src.Kind || it.Source.ContentHash != hash {
						t.Fatalf("stored source = %+v on item %+v: locator not recorded verbatim over the ingested bytes", it.Source, it)
					}
				}
			}
			if !found {
				t.Fatalf("no stored item carries locator %q", src.Locator)
			}
		}

		// Replay: the same event replays its stored receipt over the poisoned
		// locator byte-identically and writes nothing. A dereference here
		// would fail (the path is absent, the host can never resolve).
		before := f.snapshot()
		for i, src := range []domain.SourceRef{
			{Kind: domain.SourcePath, Locator: absent},
			{Kind: domain.SourceURL, Locator: url},
		} {
			again := f.mustIngest(user, event("p320-"+string(rune('a'+i)), src.Kind, src.Locator))
			if !reflect.DeepEqual(firsts[i], again) {
				t.Fatalf("replay over a poisoned locator returned a different receipt")
			}
		}
		if after := f.snapshot(); !reflect.DeepEqual(before, after) {
			t.Fatalf("replay over a poisoned locator wrote state: %+v -> %+v", before, after)
		}
	})
}
