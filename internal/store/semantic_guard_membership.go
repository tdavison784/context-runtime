package store

import "github.com/tdavison784/context-runtime/internal/domain"

func (g *semanticGuard) InsertLogicalExchange(v domain.LogicalExchange) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertLogicalExchange(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertExchangeMember(v domain.ExchangeMember) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertExchangeMember(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertExchangeAcknowledgment(v domain.ExchangeAcknowledgment) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertExchangeAcknowledgment(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertAdmissionManifest(v domain.AdmissionManifest) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertAdmissionManifest(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertCheckpoint(v domain.Checkpoint) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertCheckpoint(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) InsertOwnerRegistration(v domain.OwnerRegistration) error {
	if g.base.err != nil {
		return g.base.err
	}
	err := g.backend.InsertOwnerRegistration(v)
	g.base.noteWrite(err)
	return err
}

func (g *semanticGuard) PutLogicalExchange(v domain.LogicalExchange, expected uint64) (domain.LogicalExchange, error) {
	if g.base.err != nil {
		return domain.LogicalExchange{}, g.base.err
	}
	out, err := g.backend.PutLogicalExchange(v, expected)
	g.base.noteWrite(err)
	return out, err
}

func (g *semanticGuard) PutConversationMembership(v domain.ConversationMembershipState, expected uint64) (domain.ConversationMembershipState, error) {
	if g.base.err != nil {
		return domain.ConversationMembershipState{}, g.base.err
	}
	out, err := g.backend.PutConversationMembership(v, expected)
	g.base.noteWrite(err)
	return out, err
}
