package store

import "github.com/tdavison784/context-runtime/internal/domain"

func (g *semanticGuard) InsertResourceBinding(v domain.ResourceBinding) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertResourceBinding(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertResourceUpdate(v domain.ResourceUpdate) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertResourceUpdate(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertWorkspaceBinding(v domain.WorkspaceBinding) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertWorkspaceBinding(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertObservationRun(v domain.ObservationRun) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertObservationRun(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertObservation(v domain.ObservationRecord) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertObservation(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) PutResourceState(v domain.ResourceState, expected uint64) (domain.ResourceState, error) {
	if g.base.err != nil {
		return domain.ResourceState{}, g.base.err
	}
	out, err := g.backend.PutResourceState(v, expected)
	g.base.noteWrite(err)
	return out, err
}

func (g *semanticGuard) PutResourcePathState(v domain.ResourcePathState, expected uint64) (domain.ResourcePathState, error) {
	if g.base.err != nil {
		return domain.ResourcePathState{}, g.base.err
	}
	out, err := g.backend.PutResourcePathState(v, expected)
	g.base.noteWrite(err)
	return out, err
}

func (g *semanticGuard) PutSubjectState(v domain.SubjectState, expected uint64, causeID string) (domain.SubjectState, error) {
	if g.base.err != nil {
		return domain.SubjectState{}, g.base.err
	}
	out, err := g.backend.PutSubjectState(v, expected, causeID)
	g.base.noteWrite(err)
	return out, err
}
