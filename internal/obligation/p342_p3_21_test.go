package obligation

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
)

// TestP3_21_RawEnvironmentValuesRejected closes the P3-42 table row "raw
// environment values rejected" (ADR 8, SPEC-4.9): observations carry an
// opaque environment specification, never raw environment values. Raw,
// oversize, non-ASCII and empty specs are refused outright and persist
// nothing; only closed tokens or hashes bind, verbatim; a subject whose
// environment differs from its bound workspace never reaches an
// observation run; and the fixed obs-state/1 template is rendered from
// typed fields only, so no environment (or suite/coverage) spec becomes
// trusted template content.
func TestP3_21_RawEnvironmentValuesRejected(t *testing.T) {
	for name, backend := range map[string]func(*testing.T) store.Store{
		"memory": func(*testing.T) store.Store { return memory.New() },
		"sqlite": sqliteBackend,
	} {
		t.Run(name, func(t *testing.T) {
			if name == "sqlite" {
				// Mirror TestSQLiteSuite's probe: the facet is published, so
				// a missing family here is a regression, not pending work.
				if !familySupported(t, backend(t), func(r store.SemanticReader) error {
					_, err := r.ResourceBinding("probe")
					return err
				}) {
					t.Fatal("resource/workspace family unpublished on this backend")
				}
			}
			prev := backendFactory
			backendFactory = backend
			t.Cleanup(func() { backendFactory = prev })
			exerciseP3_21RawEnvironment(t)
		})
	}
}

func exerciseP3_21RawEnvironment(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	harness := f.harness
	task := domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task"}

	// Raw, oversize, non-ASCII and empty environment specs are rejected
	// outright and leave no binding record behind.
	for i, c := range []struct{ name, env string }{
		{"raw environment value", "PATH=/usr/bin"},
		{"oversize raw value", "HOME=/" + strings.Repeat("x", 300)},
		{"oversize closed token", strings.Repeat("env", 100)}, // 300 bytes > the 128-byte bound
		{"non-ascii token", "café-env"},
		{"empty", ""},
	} {
		id := fmt.Sprintf("wsE%d", i+1)
		in := bindIntent(id, 1, task)
		in.EnvironmentSpec = c.env
		if _, err := f.s.bindWS(t, f.st, harness, in); !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("%s: bind = %v, want ErrInvalidRecord", c.name, err)
		}
		if err := f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			if _, err := r.WorkspaceBinding(domain.WorkspaceBindingRef{ID: id, Version: 1}); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("%s: rejected spec persisted a binding: %v", c.name, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	// A closed token or a hash binds and is stored verbatim, opaque.
	for _, c := range []struct{ id, env string }{
		{"wsOpaque", "env-token-1"},
		{"wsEnvHash", hashOf("env-spec-1")},
	} {
		in := bindIntent(c.id, 1, task)
		in.EnvironmentSpec = c.env
		if _, err := f.s.bindWS(t, f.st, harness, in); err != nil {
			t.Fatalf("%s: bind = %v", c.id, err)
		}
		if err := f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			b, err := r.WorkspaceBinding(domain.WorkspaceBindingRef{ID: c.id, Version: 1})
			if err != nil || b.EnvironmentSpec != c.env {
				t.Fatalf("%s: stored binding = %+v (%v), spec must persist verbatim", c.id, b, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	// A subject whose declared environment differs from the workspace
	// binding's can never reach an observation run, so a raw value cannot
	// sneak in through the subject either.
	mismatch := runIntent("r-env-mismatch", "e-env-mismatch", testsTarget(func(v *domain.TestsTarget) { v.EnvironmentSpec = "env9" }))
	if _, err := f.registerRun(t, harness, mismatch); !errors.Is(err, domain.ErrInvalidRecord) {
		t.Errorf("subject environment mismatch: %v, want ErrInvalidRecord", err)
	}

	// The fixed obs-state/1 template is rendered from typed fields only:
	// the environment, suite and coverage specs never become trusted
	// template content.
	target := testsTarget(func(v *domain.TestsTarget) { v.EnvironmentSpec = "env-token-1" })
	sub, err := SubjectFor(target)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.ObservationRun{Subject: sub, SubjectKey: mustSubjectKey(target), TaskID: "task", Access: taskBoundary()}
	obs := domain.ObservationRecord{SemanticMeta: domain.SemanticMeta{ID: "obs-p3-21", SessionID: testSession},
		Family: domain.ObservationTests, Outcome: domain.OutcomePass, Completeness: domain.ObservationComplete,
		Passed: 3, Total: 3, ObservedWorkspaceFingerprint: hashOf("W1")}
	it := stateItem(obs, run, 7)
	text := it.Parts[0].Text
	if !strings.Contains(text, run.SubjectKey) || !strings.Contains(text, hashOf("W1")) {
		t.Errorf("state text lost its typed identity fields: %q", text)
	}
	for _, leaked := range []string{"env-token-1", "go-test-all", "CoverageSpec"} {
		if strings.Contains(text, leaked) {
			t.Errorf("state template leaked environment/suite spec %q: %q", leaked, text)
		}
	}
}
