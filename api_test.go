package contextruntime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestRootAliasesOnlyIngestInputTypes enforces R3 as an allowlist
// (SPEC-1.13: a denylist would pass a future alias, such as
// UnresolvedReference, that R3 never authorized): the root package may
// declare or alias only the named domain types and the Event/Span input
// shape below. Ingestion results, receipts, envelopes, diagnostics, and
// lifecycle command records stay internal until the SDD section 8
// Ingest signature is settled; any other new root type must extend this
// allowlist deliberately, not slip through unnoticed.
func TestRootAliasesOnlyIngestInputTypes(t *testing.T) {
	allowed := map[string]bool{
		"Principal": true, "Authority": true, "Kind": true, "Generation": true,
		"Scope": true, "Residency": true, "GoalStatus": true, "RetentionClass": true,
		"AccessBoundary": true, "ContentPart": true, "SourceRef": true, "ContextItem": true, "ItemRef": true,
		"Event": true, "EventKind": true, "Span": true, "InputPart": true, "PartType": true,
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if !allowed[ts.Name.Name] {
				t.Errorf("%s: root declares or aliases %s, not on the R3 allowlist", name, ts.Name.Name)
			}
			return true
		})
	}
}
