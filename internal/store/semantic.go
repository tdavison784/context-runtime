package store

import "github.com/tdavison784/context-runtime/internal/domain"

// ResultPage is complete for the requested page. More requires continuation at
// Next; a correctness-critical service must exhaust it or abort its transaction.
// Reads use stable (Seq, ID) order unless a method explicitly says otherwise.
type ResultPage[T any] struct {
	Records []T
	More    bool
	Next    Cursor
}

// SemanticTxBase is the Phase 3 backend facet. W2 implements it through
// SemanticBackend on its existing transaction; keeping the facet separate lets
// the frozen Phase 2 backend compile during the forward-migration handoff.
// There is no operational fallback: missing support returns ErrUnsupportedSchema.
// Every insert is append-only, session-scoped and sequenced; every Put is CAS.
// Records and pages are deep copies. Reference checks are in the schema manifest.
type SemanticTxBase interface {
	SemanticReader
	SemanticWriter
}
type SemanticTx interface {
	SemanticReader
	SemanticWriter
	Poison(error)
}
type SemanticBackendProvider interface{ SemanticBackend() SemanticTxBase }
type SemanticReadProvider interface{ SemanticReadBackend() SemanticReader }

// Semantic binds new services to the SAME transaction and poison state. It
// never opens a Store transaction and never substitutes unguarded backend writes.
type SemanticTransactionProvider interface{ SemanticTransaction() (SemanticTx, error) }

func Semantic(tx Tx) (SemanticTx, error) {
	if p, ok := tx.(SemanticTransactionProvider); ok {
		return p.SemanticTransaction()
	}
	return nil, domain.ErrUnsupportedSchema
}

func (g *Guard) SemanticTransaction() (SemanticTx, error) {
	p, ok := g.TxBase.(SemanticBackendProvider)
	if !ok {
		return nil, domain.ErrUnsupportedSchema
	}
	b := p.SemanticBackend()
	if b == nil {
		return nil, domain.ErrUnsupportedSchema
	}
	return &semanticGuard{SemanticReader: b, base: g, backend: b}, nil
}
func ReadSemantic(tx ReadTx) (SemanticReader, error) {
	if g, ok := tx.(*Guard); ok {
		tx = g.TxBase
	}
	if p, ok := tx.(SemanticReadProvider); ok {
		if r := p.SemanticReadBackend(); r != nil {
			return r, nil
		}
	}
	if p, ok := tx.(SemanticBackendProvider); ok {
		if r := p.SemanticBackend(); r != nil {
			return r, nil
		}
	}
	return nil, domain.ErrUnsupportedSchema
}

type SemanticReader interface {
	DeclarationReader
	ProofReader
	ResourceReader
	MembershipReader
	RetrievalReader
	ReceiptReader
}
type SemanticWriter interface {
	DeclarationWriter
	ProofWriter
	ResourceWriter
	MembershipWriter
	RetrievalWriter
	ReceiptWriter
}

// Every new write checks and updates the same Guard, including ignored errors.
type semanticGuard struct {
	SemanticReader
	base    *Guard
	backend SemanticTxBase
}

func (g *semanticGuard) Poison(err error) { g.base.Poison(err) }
