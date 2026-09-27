package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// replacePin emulates W3's lifecycle.ReplaceDirective inside one
// transaction: insert the new occurrence, write its creation declaration,
// supersede the prior version (retiring its obligations), then ask W4 to
// declare the replacement's obligation. declare=false skips the creation
// declaration.
func replacePin(t *testing.T, f fixture, id, dirID, text string, attrs []string, declare bool) (*domain.ObligationRef, error) {
	t.Helper()
	var ref *domain.ObligationRef
	err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
		it := storetest.NewDirective(testSession, id, dirID, tx.NextSeq(), text)
		it.Authority, it.Namespace, it.Role = domain.AuthoritySystem, domain.NamespaceDirective, domain.RoleSemantic
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		if declare {
			if _, err := graph.DeclareCreation(tx, it, graph.CreationAcceptance{PolicyVersion: "declaration/1", AcceptedAttributes: attrs}); err != nil {
				return err
			}
		}
		if _, err := graph.ReplaceDirective(tx, f.system, "task", dirID, it.ID, "evt-"+id); err != nil {
			return err
		}
		var err error
		ref, err = f.s.DeclareForReplacementTx(tx, f.system, it.ID, tx.NextSeq())
		return err
	})
	return ref, err
}

func TestDeclareForReplacement(t *testing.T) {
	f := newFixture(t)
	if _, err := f.s.bindWS(t, f.st, f.system, bindIntent("ws-sys", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task"})); err != nil {
		t.Fatal(err)
	}
	v1, err := replacePin(t, f, "r1", "rep", "Keep the build green.", []string{"obligation=tests_pass"}, true)
	if err != nil || v1 == nil || v1.Version != 1 {
		t.Fatalf("first version = %v %v", v1, err)
	}
	o1 := f.status(t, *v1)
	if o1.DeclarationKind != domain.DeclarationPinnedAttribute || o1.Claim != "tests_pass" || o1.BindingState != domain.BindingBound {
		t.Errorf("attribute declaration = %+v", o1)
	}
	// Same-content replacement (C-1): the prior version retires, the new one
	// starts UNRESOLVED with no inherited proof or grant.
	if _, err := f.s.transition(t, f.st, f.system, intent(*v1, 1, domain.ObligationSatisfied)); err != nil {
		t.Fatal(err)
	}
	v2, err := replacePin(t, f, "r2", "rep", "Keep the build green.", []string{"obligation=tests_pass"}, true)
	if err != nil || v2 == nil || v2.ObligationID != v1.ObligationID || v2.Version != 2 {
		t.Fatalf("replacement = %v %v", v2, err)
	}
	if old := f.status(t, *v1); old.Current || old.Status != domain.ObligationSatisfied {
		t.Errorf("retired v1 = %+v", old)
	}
	if cur := f.status(t, *v2); !cur.Current || cur.Status != domain.ObligationUnresolved || cur.CurrentAssertionID != "" || cur.SourceItemID != "r2" {
		t.Errorf("v2 = %+v", cur)
	}
	// Claim patterns apply to replacement text without an attribute.
	v3, err := replacePin(t, f, "r3", "rep", "All tests must pass.", nil, true)
	if err != nil || v3 == nil || v3.Version != 3 {
		t.Fatalf("pattern replacement = %v %v", v3, err)
	}
	if o := f.status(t, *v3); o.DeclarationKind != domain.DeclarationPinnedClaim {
		t.Errorf("pattern declaration = %+v", o)
	}
	// A replacement declaring nothing creates nothing.
	if ref, err := replacePin(t, f, "r4", "rep", "Ship it.", nil, true); err != nil || ref != nil {
		t.Errorf("plain replacement = %v %v", ref, err)
	}
}

func TestDeclareForReplacementFailsClosed(t *testing.T) {
	f := newFixture(t)
	// Without an immutable creation declaration the claim is unknown.
	if _, err := replacePin(t, f, "n1", "nodecl", "Keep it green.", nil, false); !errors.Is(err, domain.ErrUnsupportedSchema) {
		t.Errorf("missing creation declaration: %v", err)
	}
	// More than one obligation attribute is malformed.
	if _, err := replacePin(t, f, "n2", "two", "Keep it green.", []string{"obligation=file_read", "obligation=tests_pass"}, true); !errors.Is(err, domain.ErrInvalidRecord) {
		t.Errorf("two obligation attributes: %v", err)
	}
	// Only an occurrence created in this transaction is declared.
	old := seedPinned(t, f.st, "o1", "old", domain.AuthoritySystem, "All tests must pass.")
	err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
		_, err := f.s.DeclareForReplacementTx(tx, f.system, old.ID, tx.NextSeq())
		return err
	})
	if !errors.Is(err, domain.ErrInvalidRecord) {
		t.Errorf("earlier occurrence: %v", err)
	}
}
