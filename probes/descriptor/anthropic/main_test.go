package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SEC-1.6: the markdown report must go through the same redaction as the
// JSON twin; notes carry raw err.Error() text from aborted probe groups.
func TestWriteReportSanitizesMarkdown(t *testing.T) {
	const leaked = "sk-ant-api03-LEAKED"
	rec := &Recorder{}
	rec.Note(Observation{ID: "group/aborted", Note: "transport failed: x-api-key " + leaked})
	rec.Note(Observation{ID: "group/err", Error: "Bearer " + leaked})
	path := filepath.Join(t.TempDir(), "observations.md")
	if err := writeReport(path, rec); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, strings.TrimSuffix(path, ".md") + ".json"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), leaked) || strings.Contains(strings.ToLower(string(b)), "sk-ant-") {
			t.Errorf("%s contains key material:\n%s", filepath.Base(p), b)
		}
	}
}
