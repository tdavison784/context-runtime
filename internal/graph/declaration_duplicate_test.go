package graph

import (
	"errors"
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"testing"
)

type declarationFixture struct {
	store.ReadTx
	store.SemanticReader
	declarations map[string]domain.CreationDeclaration
}

func (f *declarationFixture) SemanticReadBackend() store.SemanticReader { return f }
func (f *declarationFixture) CreationDeclaration(id string) (domain.CreationDeclaration, error) {
	d, ok := f.declarations[id]
	if !ok {
		return d, domain.ErrNotFound
	}
	return d.Clone(), nil
}
func declarationForTest(item domain.ContextItem) domain.CreationDeclaration {
	key, _ := item.CurrentKey()
	s := domain.CreationSemantics{Key: key, Authority: item.Authority, WorkflowID: item.WorkflowID, AgentID: item.AgentID, Section: item.Section, Kind: item.Kind, ContentHash: item.ContentHash, Generation: item.Generation, Retention: item.Retention, Residency: item.Residency, GoalStatus: item.GoalStatus, OriginTaskID: item.TaskID, OriginTurnID: item.TurnID, CreatedTurn: item.CreatedTurn, TTLTurns: item.TTLTurns}
	h, _ := s.Signature("policy-1")
	return domain.CreationDeclaration{SemanticMeta: domain.SemanticMeta{ID: "decl-" + item.ID, SessionID: item.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: item.Seq}, ItemID: item.ID, PolicyVersion: "policy-1", LegacyKnown: true, Signature: h, AcceptedSemantics: s.Clone()}
}
func TestDeclarationDedupSurvivesLifecycleAndPolicyChanges(t *testing.T) {
	prior := goalLike("s", "prior", "goal", 1, "Ship it")
	fresh := goalLike("s", "fresh", "goal", 2, "Ship it")
	f := &declarationFixture{declarations: map[string]domain.CreationDeclaration{prior.ID: declarationForTest(prior), fresh.ID: declarationForTest(fresh)}}
	resolved := domain.GoalResolved
	prior.GoalStatus, prior.Generation, prior.Retention, prior.Residency = &resolved, domain.GenerationDurable, domain.RetentionHigh, domain.ResidencyArchived
	d := f.declarations[fresh.ID]
	d.PolicyVersion = "policy-2"
	d.Signature, _ = d.AcceptedSemantics.Signature(d.PolicyVersion)
	f.declarations[fresh.ID] = d
	if same, err := SameDirective(f, fresh, "untrusted-string", prior); err != nil || !same {
		t.Fatalf("restatement = %v, %v", same, err)
	}
	for _, change := range []func(*domain.CreationDeclaration){
		func(d *domain.CreationDeclaration) { d.AcceptedSemantics.SupportIDs = []string{"evidence"} },
		func(d *domain.CreationDeclaration) {
			d.AcceptedSemantics.AcceptedAttributes = []string{"retention=high"}
		},
		func(d *domain.CreationDeclaration) {
			d.AcceptedSemantics.ObligationDeclarationHash = domain.HashBytes([]byte("new claim"))
		},
	} {
		changed := d.Clone()
		change(&changed)
		changed.Signature, _ = changed.AcceptedSemantics.Signature(changed.PolicyVersion)
		f.declarations[fresh.ID] = changed
		if same, err := SameDirective(f, fresh, "", prior); err != nil || same {
			t.Fatalf("changed declaration = %v, %v", same, err)
		}
	}
	delete(f.declarations, prior.ID)
	if same, err := SameDirective(f, fresh, "", prior); same || !errors.Is(err, ErrUnknownDeclaration) {
		t.Fatal("unknown legacy declaration must fail closed, never match or silently differ (SPEC-1.3)", same, err)
	}
}
