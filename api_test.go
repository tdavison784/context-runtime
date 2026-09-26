package contextruntime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestRootAliasesExcludeIngestResults enforces R3: the root package may alias
// the Event/Span input types, but not ingestion results, receipts, envelopes,
// diagnostics, or lifecycle command records, which stay internal until the
// SDD section 8 Ingest signature is settled.
func TestRootAliasesExcludeIngestResults(t *testing.T) {
	forbidden := map[string]bool{
		"IngestReceipt": true, "IngestResult": true, "IngestLink": true, "EventEnvelope": true, "ExecutionVersions": true,
		"Diagnostic": true, "DiagnosticRecord": true, "DiagnosticCode": true, "LifecycleCommand": true, "LifecycleCommandRecord": true,
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
			if forbidden[ts.Name.Name] {
				t.Errorf("%s: root declares %s", name, ts.Name.Name)
			}
			if sel, ok := ts.Type.(*ast.SelectorExpr); ok && forbidden[sel.Sel.Name] {
				t.Errorf("%s: root aliases %s", name, sel.Sel.Name)
			}
			return true
		})
	}
}
