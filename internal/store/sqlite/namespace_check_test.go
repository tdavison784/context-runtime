package sqlite

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// domainNamespaces parses internal/domain for every constant of type
// DirectiveNamespace, so a namespace added to the domain is found without
// anyone remembering to list it here.
func domainNamespaces(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "domain", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("domain sources: %v", err)
	}
	var out []string
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			g, ok := decl.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				vs := spec.(*ast.ValueSpec)
				if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "DirectiveNamespace" {
					continue
				}
				for _, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok {
						t.Fatalf("%s: non-literal DirectiveNamespace constant", name)
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					out = append(out, s)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// TestCurrentKeyNamespacesMatchDomain checks that the current-version
// table's namespace CHECK admits exactly the domain's namespaces (P3-3):
// each is valid in the domain, and a namespace added to the domain fails
// here until a forward migration admits it.
func TestCurrentKeyNamespacesMatchDomain(t *testing.T) {
	want := domainNamespaces(t)
	if len(want) == 0 {
		t.Fatal("no DirectiveNamespace constants found in internal/domain")
	}
	for _, ns := range want {
		if !domain.DirectiveNamespace(ns).Valid() {
			t.Errorf("domain constant %q is not a valid namespace", ns)
		}
	}
	s, _ := openTemp(t)
	var ddl string
	if err := s.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='directives'").Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`CHECK\s*\(\s*namespace\s+IN\s*\(([^)]*)\)\s*\)`).FindStringSubmatch(ddl)
	if m == nil {
		t.Fatalf("directives table has no namespace CHECK: %s", ddl)
	}
	var got []string
	for _, q := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[1], -1) {
		got = append(got, q[1])
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("directives CHECK admits %v, domain namespaces are %v", got, want)
	}
}

// TestObservationCurrentPointer files an OBSERVATION-namespace current
// pointer through both current-version writes (P3-3, P3-22).
func TestObservationCurrentPointer(t *testing.T) {
	s, _ := openTemp(t)
	obs := storetest.NewItem("s", "obs", 0, "observed state")
	obs.Authority, obs.Kind, obs.Scope = domain.AuthorityTool, domain.KindTaskState, domain.ScopeTask
	obs.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task"}
	obs.Namespace, obs.DirectiveID = domain.NamespaceObservation, "sub_"+"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		obs.Seq = tx.NextSeq()
		if err := tx.InsertItem(obs); err != nil {
			return err
		}
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		return sem.SetCurrentVersion("obs", "")
	}); err != nil {
		t.Fatalf("file OBSERVATION pointer: %v", err)
	}
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		key, _ := obs.CurrentKey()
		id, err := tx.CurrentVersion(key)
		if err != nil || id != "obs" {
			t.Errorf("CurrentVersion(OBSERVATION) = %q, %v", id, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
