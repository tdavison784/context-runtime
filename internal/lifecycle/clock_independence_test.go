package lifecycle

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Clock independence (P3-31, P3-39, SPEC-6.6a): every decision in policy
// (eligibility, leases, GC), lifecycle (completion, GC, replay) and the
// stores' read paths is derived from stored turn/sequence state, never from
// the wall clock, so the same committed state replays identically offline,
// at any speed, at any time of day. time.Now and time.Since are the two
// stdlib reads that break that; a clock, if one is ever truly needed, must
// be injected by the embedding (like ingest's Now seam), which this check
// still permits because the seam lives outside these trees.
//
// The check is static and go/ast-based: it fails on any time.Now/time.Since
// reference — called, compared or passed as a value — in the non-test files
// of internal/policy, internal/lifecycle, internal/store/** and
// internal/gcqueue, and on a dot-import of time, which would defeat the
// selector check.
func TestP3_31_P3_39_NoWallClockReadsInPolicyLifecycleStoreGcqueue(t *testing.T) {
	// roots are the four clock-free trees, relative to this package's dir.
	roots := map[string]string{
		"internal/lifecycle": ".",
		"internal/policy":    "../policy",
		"internal/store/**":  "../store",
		"internal/gcqueue":   "../gcqueue",
	}
	seen := map[string]int{}
	for label, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(path)
			if d.IsDir() {
				if path != root && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
				return nil
			}
			seen[label]++
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			// Which identifier is 'time' in this file, if any.
			timeName := "time"
			hasTime := false
			for _, imp := range f.Imports {
				p, err := strconv.Unquote(imp.Path.Value)
				if err != nil || p != "time" {
					continue
				}
				hasTime = true
				switch {
				case imp.Name == nil:
					// plain import "time"
				case imp.Name.Name == "_":
					// blank import: references nothing
					hasTime = false
				case imp.Name.Name == ".":
					t.Errorf("%s: dot-import of time defeats the clock check", rel)
				default:
					timeName = imp.Name.Name
				}
			}
			if !hasTime {
				return nil
			}
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != "Now" && sel.Sel.Name != "Since" {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == timeName {
					t.Errorf("%s: %s.%s reads the wall clock (P3-31/P3-39 clock independence)", rel, timeName, sel.Sel.Name)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if seen[label] == 0 {
			t.Fatalf("%s: no non-test Go files found — clock check is vacuous", label)
		}
	}
}
