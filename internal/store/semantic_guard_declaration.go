package store

import "github.com/tdavison784/context-runtime/internal/domain"

func (g *semanticGuard) InsertCreationDeclaration(v domain.CreationDeclaration) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertCreationDeclaration(v)
	g.base.noteWrite(err)
	return err
}
func (g *semanticGuard) InsertSnapshotDeclaration(v domain.SnapshotDeclaration) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertSnapshotDeclaration(v)
	g.base.noteWrite(err)
	return err
}
func (g *semanticGuard) InsertCoverage(v domain.CoverageRecord, members []domain.CoverageMember) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertCoverage(v, members)
	g.base.noteWrite(err)
	return err
}
func (g *semanticGuard) SetCurrentVersion(id, prior string) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.SetCurrentVersion(id, prior)
	g.base.noteWrite(err)
	return err
}
func (g *semanticGuard) InsertSemanticChange(v domain.SemanticChange) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertSemanticChange(v)
	g.base.noteWrite(err)
	return err
}
