package obligation

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func TestRegistryExactVersions(t *testing.T) {
	r := DefaultRegistry()
	for _, ref := range []domain.MatcherRef{TestsPassV1, FileReadV1} {
		m, ok := r.Lookup(ref)
		if !ok || m.Ref() != ref || string(m.Family()) != ref.Name {
			t.Errorf("Lookup(%v) = %v, %v", ref, m, ok)
		}
	}
	for _, ref := range []domain.MatcherRef{
		{Name: "tests_pass", Version: "2"},
		{Name: "tests_pass", Version: ""},
		{Name: "tests_pass", Version: "latest"},
		{Name: "file_read", Version: "0"},
		{Name: "shell_ok", Version: "1"},
		{},
	} {
		if m, ok := r.Lookup(ref); ok {
			t.Errorf("Lookup(%v) resolved to %v; unknown versions must stay unresolved", ref, m.Ref())
		}
	}
}

func TestRegistryForClaim(t *testing.T) {
	r := DefaultRegistry()
	if m, ok := r.ForClaim("tests_pass"); !ok || m.Ref() != TestsPassV1 {
		t.Errorf("ForClaim(tests_pass) = %v, %v", m, ok)
	}
	if m, ok := r.ForClaim("file_read"); !ok || m.Ref() != FileReadV1 {
		t.Errorf("ForClaim(file_read) = %v, %v", m, ok)
	}
	for _, name := range []string{"", "Tests_pass", "tests_pass.1", "deploy_ok"} {
		if _, ok := r.ForClaim(name); ok {
			t.Errorf("ForClaim(%q) matched an unregistered claim", name)
		}
	}
}
