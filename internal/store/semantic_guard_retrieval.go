package store

import "github.com/tdavison784/context-runtime/internal/domain"

func (g *semanticGuard) InsertRetrievalLease(v domain.RetrievalLease) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertRetrievalLease(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertRetrievalResult(v domain.RetrievalResult) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertRetrievalResult(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertRetrievalEvent(v domain.RetrievalEvent) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertRetrievalEvent(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertProjection(v domain.ProjectionRecord) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertProjection(v)
	g.base.noteWrite(err)
	return err
}
