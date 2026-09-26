package contextruntime

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/tdavison784/context-runtime"

// TestPackageBoundaries enforces the dependency rules of SDD section 6 on
// non-test files. The planner must stay provider-independent: it may not
// reach capability descriptors, strategies, or adapters, even transitively.
func TestPackageBoundaries(t *testing.T) {
	graph := moduleImports(t)

	// Direct-import allow lists for module-internal packages.
	allowOnly := map[string][]string{
		"internal/domain":    {},
		"internal/directive": {"internal/domain"},
		"internal/provider":  {"internal/domain"},
		"internal/telemetry": {"internal/domain"},
	}
	// Policy is pure like the parser. Ingestion reaches stores only through
	// the internal/store interface, never a concrete store. The root package
	// re-exports domain types and nothing else.
	allowOnly["internal/policy"] = []string{"internal/domain"}
	allowOnly["internal/ingest"] = []string{"internal/domain", "internal/directive", "internal/policy", "internal/store", "internal/graph", "internal/lifecycle", "internal/obligation", "internal/tools", "internal/retrieve"}
	allowOnly["internal/graph"] = []string{"internal/domain", "internal/store"}
	allowOnly["internal/obligation"] = []string{"internal/domain", "internal/store", "internal/graph", "internal/policy"}
	allowOnly["internal/lifecycle"] = []string{"internal/domain", "internal/store", "internal/graph", "internal/policy", "internal/obligation"}
	allowOnly["internal/tools"] = []string{"internal/domain", "internal/store", "internal/graph", "internal/policy", "internal/retrieve"}
	allowOnly["internal/retrieve"] = []string{"internal/domain", "internal/store", "internal/graph", "internal/policy"}
	allowOnly["."] = []string{"internal/domain"}
	for pkg, allowed := range allowOnly {
		for _, dep := range graph[pkg] {
			if !slices.Contains(allowed, dep) {
				t.Errorf("%s imports %s; allowed module imports: %v", pkg, dep, allowed)
			}
		}
	}

	// Stores depend only on the domain and on each other.
	for pkg, deps := range graph {
		if pkg != "internal/store" && !strings.HasPrefix(pkg, "internal/store/") {
			continue
		}
		for _, dep := range deps {
			if dep != "internal/domain" && dep != "internal/store" && !strings.HasPrefix(dep, "internal/store/") {
				t.Errorf("%s imports %s; stores may import only internal/domain and internal/store/...", pkg, dep)
			}
		}
	}

	// Transitive prohibitions: what the planner decides must not depend on
	// how a provider receives it.
	forbidden := []string{"internal/capability", "internal/strategy", "internal/provider"}
	for _, dep := range transitive(graph, "internal/plan") {
		if slices.Contains(forbidden, dep) {
			t.Errorf("internal/plan transitively imports %s", dep)
		}
	}

	// Nothing inside the module imports the public root package.
	for pkg, deps := range graph {
		if slices.Contains(deps, ".") {
			t.Errorf("%s imports the root package", pkg)
		}
	}
}

// moduleImports maps each package directory (relative to the module root,
// "." for the root) to its module-internal imports from non-test files.
func moduleImports(t *testing.T) map[string][]string {
	t.Helper()
	graph := map[string][]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != "." && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(filepath.Dir(path))
		if _, ok := graph[pkg]; !ok {
			graph[pkg] = nil
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			var rel string
			switch {
			case p == modulePath:
				rel = "."
			case strings.HasPrefix(p, modulePath+"/"):
				rel = strings.TrimPrefix(p, modulePath+"/")
			default:
				continue
			}
			if !slices.Contains(graph[pkg], rel) {
				graph[pkg] = append(graph[pkg], rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("internal/domain"); err != nil {
		t.Fatalf("run from the module root: %v", err)
	}
	return graph
}

func transitive(graph map[string][]string, from string) []string {
	var out []string
	seen := map[string]bool{from: true}
	stack := []string{from}
	for len(stack) > 0 {
		pkg := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, dep := range graph[pkg] {
			if !seen[dep] {
				seen[dep] = true
				out = append(out, dep)
				stack = append(stack, dep)
			}
		}
	}
	return out
}
