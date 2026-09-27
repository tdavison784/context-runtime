package ingest

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEveryFuzzTargetRunsInCI (TEST-1.3, gate item 14): every fuzz target in
// the module is named in the CI fuzz job, so a new target cannot silently
// run only its inline seeds.
func TestEveryFuzzTargetRunsInCI(t *testing.T) {
	root := filepath.Join("..", "..")
	ci, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	decl := regexp.MustCompile(`(?m)^func (Fuzz\w+)\(f \*testing\.F\)`)
	var found int
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		pkg := "./" + filepath.ToSlash(filepath.Dir(must(filepath.Rel(root, path))))
		for _, m := range decl.FindAllStringSubmatch(string(b), -1) {
			found++
			if !strings.Contains(string(ci), `"`+m[1]+" "+pkg+`"`) {
				t.Errorf("%s (%s) is not in the CI fuzz job", m[1], pkg)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("no fuzz targets found; the scan is broken")
	}
}

func must(s string, err error) string {
	if err != nil {
		panic(err)
	}
	return s
}
