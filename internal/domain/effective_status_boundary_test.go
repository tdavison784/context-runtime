package domain

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// K1 A2: an obligation's effective status comes from ONE helper,
// obligation.EffectiveStatus. No other production code may read the stored
// Status field of an ObligationVersion, however it is used — compared,
// assigned to a local, a map key, converted, a method value — because a
// stored SATISFIED resource-bound version may be effectively UNRESOLVED.
// The check is type-based (SPEC-5.9), so an alias through a local variable
// or any other indirection is caught. The domain package (record
// validation), internal/store (guards and backends) and test files are
// exempt; the transition table compares requested from/to values, not
// stored status.

const boundaryModule = "github.com/tdavison784/context-runtime"

// statusReadAllowlist: functions allowed to read the stored status, and why.
// A read that selects or branches on the status is NEVER allowlisted; only
// the helper itself, the write-side transition machinery, and mechanical
// copies that quote the status as data.
var statusReadAllowlist = map[string]string{
	"internal/obligation:EffectiveStatus":           "the one K1 A2 effective-status helper",
	"internal/obligation:Service.ApplyTransitionTx": "the write path: transition-table check, recorded cause and history (K1 A2's transition-table exemption; a pending version is settled first)",
	"internal/graph:settleBeforeRetirement":         "M2 settlement pre-check before retirement (settlement machinery, like the store guards)",
	"internal/lifecycle:completionBlockers":         "Status.Valid() enum shape validation only; selection goes through openObligation -> effectiveStatus",
}

// statusReadPending lists stored-status reads that predate K1 and must move
// onto obligation.EffectiveStatus. It may only shrink: a new read fails, and
// an entry that no longer occurs fails until it is removed here.
var statusReadPending = map[string]string{}

// TestEffectiveStatusIsTheOnlyStoredStatusReader_K1A2 type-checks every
// production package (domain and store excepted) and fails on any read of
// ObligationVersion.Status outside the allowlist.
func TestEffectiveStatusIsTheOnlyStoredStatusReader_K1A2(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "source", nil)
	dom, err := imp.Import(boundaryModule + "/internal/domain")
	if err != nil {
		t.Fatalf("type-check domain: %v", err)
	}
	tn, ok := dom.Scope().Lookup("ObligationVersion").(*types.TypeName)
	if !ok {
		t.Fatalf("ObligationVersion is a %T", dom.Scope().Lookup("ObligationVersion"))
	}
	st, ok := tn.Type().Underlying().(*types.Struct)
	if !ok {
		t.Fatalf("ObligationVersion underlying is a %T", tn.Type().Underlying())
	}
	var statusField types.Object
	for i := range st.NumFields() {
		if f := st.Field(i); f.Name() == "Status" {
			statusField = f
		}
	}
	if statusField == nil {
		t.Fatal("ObligationVersion has no Status field")
	}

	// Group the production files by package directory.
	pkgs := map[string][]string{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" ||
				rel == "internal/domain" || rel == "internal/store") {
				return filepath.SkipDir
			}
			// Nested modules (probes/) are outside this module's build.
			if path != root {
				if _, err := stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		if dir == "." {
			dir = ""
		}
		pkgs[dir] = append(pkgs[dir], path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for dir := range pkgs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	found := map[string][]string{}
	for _, dir := range dirs {
		files := make([]*ast.File, len(pkgs[dir]))
		for i, path := range pkgs[dir] {
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			files[i] = f
		}
		pkgPath := boundaryModule + "/" + dir
		if dir == "" {
			pkgPath = boundaryModule
		}
		info := &types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}}
		conf := types.Config{Importer: imp, Error: func(error) {}}
		if _, err := conf.Check(pkgPath, fset, files, info); err != nil {
			// Fail closed: a package that does not type-check could hide a read.
			t.Fatalf("%s: %v", pkgPath, err)
		}
		funcs := map[token.Pos]string{}
		for _, f := range files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				name := fn.Name.Name
				if fn.Recv != nil && len(fn.Recv.List) == 1 {
					name = receiverName(fn.Recv.List[0].Type) + "." + name
				}
				funcs[fn.Pos()] = name
			}
		}
		for sel, selection := range info.Selections {
			if selection.Kind() != types.FieldVal || selection.Obj() != statusField {
				continue
			}
			name := "(top-level)"
			var at token.Pos
			for pos, fn := range funcs {
				if pos <= sel.Pos() && (name == "(top-level)" || pos > at) {
					name, at = fn, pos
				}
			}
			key := dir + ":" + name
			found[key] = append(found[key], fset.Position(sel.Pos()).String())
		}
	}
	var keys []string
	for k := range found {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := statusReadAllowlist[k]; ok {
			continue
		}
		if _, ok := statusReadPending[k]; ok {
			continue
		}
		t.Errorf("%s reads the stored obligation status at %v; read it through obligation.EffectiveStatus (K1 A2)", k, found[k])
	}
	for k, why := range statusReadPending {
		if _, ok := found[k]; !ok {
			t.Errorf("pending K1 A2 entry %q (%s) no longer occurs; remove it from statusReadPending", k, why)
		}
	}
}

func receiverName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return receiverName(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return receiverName(x.X)
	}
	return "?"
}

// stat is os.Stat for the nested-module check.
var stat = os.Stat
