package domain

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// K1 A2: an obligation's effective status comes from ONE helper,
// obligation.EffectiveStatus. No other production code may select or branch
// on the stored status of an obligation version (X.Status compared with an
// obligation-status constant): a stored SATISFIED resource-bound version may
// be effectively UNRESOLVED. The transition table compares requested from/to
// values, not stored status, and is not affected; the domain package (record
// validation) and internal/store (guards and backends) are allowed.

// obligationStatusConstants are the ObligationStatus constant names.
var obligationStatusConstants = map[string]bool{
	"ObligationUnresolved": true, "ObligationSatisfied": true, "ObligationBlocked": true, "ObligationWaived": true,
}

// effectiveStatusAllowed are the functions allowed to compare stored status.
var effectiveStatusAllowed = map[string]string{
	"internal/obligation:EffectiveStatus":   "the one K1 A2 effective-status helper",
	"internal/graph:settleBeforeRetirement": "M2 settlement pre-check before retirement (settlement machinery, like the store guards)",
}

// effectiveStatusPending lists stored-status comparisons that predate K1 and
// must move onto obligation.EffectiveStatus (W3c in internal/lifecycle; W4b
// finished internal/obligation at 51a09a1). It may only shrink: a new comparison fails, and an
// entry that no longer occurs fails until it is removed here.
var effectiveStatusPending = map[string]string{}

func TestEffectiveStatusIsTheOnlyStoredStatusReader_K1A2(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || rel == "internal/domain" || rel == "internal/store" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				name = receiverName(fn.Recv.List[0].Type) + "." + name
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BinaryExpr:
					if (x.Op == token.EQL || x.Op == token.NEQ) &&
						(isStatusField(x.X) && isStatusConstant(x.Y) || isStatusField(x.Y) && isStatusConstant(x.X)) {
						found[rel+":"+name] = append(found[rel+":"+name], fset.Position(x.Pos()).String())
					}
				case *ast.SwitchStmt:
					if isStatusField(x.Tag) {
						for _, c := range x.Body.List {
							for _, e := range c.(*ast.CaseClause).List {
								if isStatusConstant(e) {
									found[rel+":"+name] = append(found[rel+":"+name], fset.Position(e.Pos()).String())
								}
							}
						}
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range found {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		dir := k[:strings.LastIndex(k[:strings.Index(k, ":")], "/")]
		fnName := k[strings.Index(k, ":")+1:]
		if i := strings.LastIndex(fnName, "."); i >= 0 {
			fnName = fnName[i+1:]
		}
		if _, ok := effectiveStatusAllowed[dir+":"+fnName]; ok {
			continue
		}
		if _, ok := effectiveStatusPending[k]; ok {
			continue
		}
		t.Errorf("%s compares stored obligation status at %v; read it through obligation.EffectiveStatus (K1 A2)", k, found[k])
	}
	for k, why := range effectiveStatusPending {
		if _, ok := found[k]; !ok {
			t.Errorf("pending K1 A2 entry %q (%s) no longer occurs; remove it from effectiveStatusPending", k, why)
		}
	}
}

func isStatusField(e ast.Expr) bool {
	s, ok := e.(*ast.SelectorExpr)
	return ok && s.Sel.Name == "Status"
}

func isStatusConstant(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		return obligationStatusConstants[x.Sel.Name]
	case *ast.Ident:
		return obligationStatusConstants[x.Name]
	}
	return false
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
