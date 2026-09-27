package store

import "github.com/tdavison784/context-runtime/internal/domain"

func (g *semanticGuard) InsertMutationReceipt(v domain.MutationReceipt) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertMutationReceipt(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertToolExecutionReceipt(v domain.ToolExecutionReceipt) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertToolExecutionReceipt(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertGCRequest(v domain.GCRequest) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertGCRequest(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertGCResult(v domain.GCResult) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertGCResult(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertCollectReceipt(v domain.CollectReceipt) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertCollectReceipt(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) PutGCProgress(v domain.GCProgress, expected uint64) (domain.GCProgress, error) {
	if g.base.err != nil {
		return domain.GCProgress{}, g.base.err
	}
	out, err := g.backend.PutGCProgress(v, expected)
	g.base.noteWrite(err)
	return out, err
}
