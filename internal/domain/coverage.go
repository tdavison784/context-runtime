package domain

// Coverage purposes are disjoint capabilities, never inferred from edge type.
type CoveragePurpose string

const (
	CoverageProvenance          CoveragePurpose = "PROVENANCE"
	CoverageEvidenceSupport     CoveragePurpose = "EVIDENCE_SUPPORT"
	CoverageRepresentation      CoveragePurpose = "REPRESENTATION"
	CoverageLeaseDependency     CoveragePurpose = "LEASE_DEPENDENCY"
	CoverageExchangeReplacement CoveragePurpose = "EXCHANGE_REPLACEMENT"
	CoverageGenerationInput     CoveragePurpose = "GENERATION_INPUT"
)

func (p CoveragePurpose) Valid() bool {
	switch p {
	case CoverageProvenance, CoverageEvidenceSupport, CoverageRepresentation, CoverageLeaseDependency, CoverageExchangeReplacement, CoverageGenerationInput:
		return true
	}
	return false
}

type ItemContentRef struct{ ItemID, ContentHash string }

func (r ItemContentRef) Validate() error {
	if !semanticID(r.ItemID) || !ValidHash(r.ContentHash) {
		return invalid("content reference: occurrence and content hash required")
	}
	return nil
}

// CoverageMember is an indexed normalized row, not copied onto each edge.
// Exactly one member form is set. Lease members always preserve source identity.
type CoverageMember struct {
	SessionID, CoverageID                 string
	Source                                *ItemContentRef
	LeaseID, NestedCoverageID, ExchangeID string
}

func (m CoverageMember) Clone() CoverageMember {
	if m.Source != nil {
		v := *m.Source
		m.Source = &v
	}
	return m
}
func (m CoverageMember) Key() (string, error) {
	if !semanticID(m.SessionID) || !semanticID(m.CoverageID) {
		return "", invalid("coverage member: session and parent required")
	}
	n := 0
	if m.Source != nil {
		n++
	}
	if m.NestedCoverageID != "" {
		n++
	}
	if m.ExchangeID != "" {
		n++
	}
	if n != 1 || m.LeaseID != "" && m.Source == nil || m.NestedCoverageID == m.CoverageID {
		return "", invalid("coverage member: invalid tagged reference")
	}
	e := NewCanonicalEncoder("context-runtime/coverage-member/v1").String(m.SessionID).Uint(boolUint(m.Source != nil))
	if m.Source != nil {
		if err := m.Source.Validate(); err != nil {
			return "", err
		}
		e.String(m.Source.ItemID).String(m.Source.ContentHash)
	}
	return e.String(m.LeaseID).String(m.NestedCoverageID).String(m.ExchangeID).Hash(), nil
}
func (m CoverageMember) Validate() error { _, err := m.Key(); return err }

type CoverageRecord struct {
	SemanticMeta
	Purpose                            CoveragePurpose
	Access                             AccessBoundary
	ConversationID                     string
	MembershipRevision, ClosedFrontier uint64
	MemberCount                        uint64
	Signature                          string
}

func (c CoverageRecord) Validate() error {
	if err := c.SemanticMeta.Validate(); err != nil {
		return err
	}
	if !c.Purpose.Valid() || !ValidHash(c.Signature) || c.MemberCount == 0 {
		return invalid("coverage: purpose, complete members, and signature required")
	}
	if c.Purpose == CoverageExchangeReplacement && (c.ConversationID == "" || c.MembershipRevision == 0 || c.ClosedFrontier == 0) {
		return invalid("coverage: closed frontier required")
	}
	return semanticBoundary(c.SessionID, c.Access)
}

// CoverageSignature requires already sorted unique member keys; services may
// canonicalize sets before this call, but stores must reject duplicates.
func CoverageSignature(c CoverageRecord, members []CoverageMember) (string, error) {
	if uint64(len(members)) != c.MemberCount || !c.Purpose.Valid() {
		return "", ErrIncompleteCoverage
	}
	e := NewCanonicalEncoder("context-runtime/coverage/v1").String(c.SessionID).String(string(c.Purpose))
	encodeBoundary(e, c.Access)
	e.String(c.ConversationID).Uint(c.MembershipRevision).Uint(c.ClosedFrontier).Uint(c.MemberCount)
	prior := ""
	for _, m := range members {
		key, err := m.Key()
		if err != nil {
			return "", err
		}
		if m.SessionID != c.SessionID || m.CoverageID != c.ID || key <= prior {
			return "", invalid("coverage: mismatched or unordered member")
		}
		if c.Purpose == CoverageExchangeReplacement && m.ExchangeID == "" {
			return "", invalid("coverage: exchange member required")
		}
		prior = key
		e.String(key)
	}
	return e.Hash(), nil
}
