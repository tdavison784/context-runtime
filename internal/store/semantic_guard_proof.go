package store

import "github.com/tdavison784/context-runtime/internal/domain"

func (g *semanticGuard) InsertObligationDeclaration(v domain.ObligationDeclaration) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertObligationDeclaration(v)
	g.base.noteWrite(err)
	return err
}
func (g *semanticGuard) InsertApplicabilityProof(v domain.ApplicabilityProof, deps []domain.ProofDependency) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertApplicabilityProof(v, deps)
	g.base.noteWrite(err)
	return err
}
func (g *semanticGuard) InsertAssertion(v domain.AssertionRecord) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertAssertion(v)
	g.base.noteWrite(err)
	return err
}
func (g *semanticGuard) AppendSemanticObligationTransition(t domain.ObligationTransition, d domain.TransitionDetail, expected uint64) (domain.ObligationVersion, error) {
	if g.base.err != nil {
		return domain.ObligationVersion{}, g.base.err
	}
	v, err := g.backend.AppendSemanticObligationTransition(t, d, expected)
	g.base.noteWrite(err)
	return v, err
}
func (g *semanticGuard) SetObligationMaterialization(target domain.ObligationRef, disabled bool, expected uint64, event domain.LifecycleEvent) (domain.ObligationVersion, error) {
	if g.base.err != nil {
		return domain.ObligationVersion{}, g.base.err
	}
	v, err := g.backend.SetObligationMaterialization(target, disabled, expected, event)
	g.base.noteWrite(err)
	return v, err
}
