package sqlite

import "github.com/tdavison784/context-runtime/internal/domain"

// obligationDeclarationRow is the declaration of one exact obligation
// version, keyed by (obligation, version) (P3-12).
type obligationDeclarationRow struct {
	SessionID    string
	ObligationID string
	Version      int
	Declaration  domain.ObligationDeclaration
}
