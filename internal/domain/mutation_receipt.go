package domain

import "slices"

type MutationFamily string

const (
	MutationLifecycle   MutationFamily = "LIFECYCLE"
	MutationObligation  MutationFamily = "OBLIGATION"
	MutationGrantFamily MutationFamily = "GRANT"
	MutationResource    MutationFamily = "RESOURCE"
	MutationTool        MutationFamily = "TOOL"
	MutationRetrieval   MutationFamily = "RETRIEVAL"
	MutationCollection  MutationFamily = "COLLECTION"
	MutationMembership  MutationFamily = "MEMBERSHIP"
)

func (f MutationFamily) Valid() bool { return f.HashDomain() != "" }

func MutationRequestHash(principal Principal, family MutationFamily, method string, args []byte) (string, error) {
	if err := validateIngestPrincipal(principal); err != nil {
		return "", err
	}
	if !family.Valid() || !semanticID(method) || len(args) == 0 {
		return "", invalid("mutation identity: family, method, and canonical arguments required")
	}
	e := NewCanonicalEncoder(family.HashDomain())
	encodePrincipal(e, principal)
	return e.String(string(family)).String(method).Bytes(args).Hash(), nil
}

type MutationReceipt struct {
	SemanticMeta
	Family                                         MutationFamily
	RequestID                                      string
	Principal                                      Principal
	CanonicalMethod                                string
	CanonicalArguments                             []byte
	RequestHashVersion, RequestHash, PolicyVersion string
	Result                                         MutationResult
}

func (r MutationReceipt) Clone() MutationReceipt {
	r.CanonicalArguments = slices.Clone(r.CanonicalArguments)
	r.Result = r.Result.Clone()
	return r
}
func (r MutationReceipt) Validate() error {
	if err := r.SemanticMeta.Validate(); err != nil {
		return err
	}
	if !semanticID(r.RequestID) || !semanticID(r.PolicyVersion) || r.RequestHashVersion != RequestHashV3 || r.Principal.SessionID != r.SessionID {
		return invalid("mutation receipt: identity/version mismatch")
	}
	h, err := MutationRequestHash(r.Principal, r.Family, r.CanonicalMethod, r.CanonicalArguments)
	if err != nil {
		return err
	}
	if h != r.RequestHash {
		return ErrIntegrity
	}
	return r.Result.Validate()
}

// CheckReplay precedes target/current-state checks. It never recomputes effects.
func (r MutationReceipt) CheckReplay(principal Principal, family MutationFamily, method string, args []byte) error {
	h, err := MutationRequestHash(principal, family, method, args)
	if err != nil || r.Principal != principal || r.Family != family || r.CanonicalMethod != method || r.RequestHashVersion != RequestHashV3 || h != r.RequestHash {
		return ErrEventIDConflict
	}
	return r.Validate()
}

type ToolExecutionReceipt struct {
	SemanticMeta
	Invocation                             ToolInvocation
	Method, MutationReceiptID, RequestHash string
	Result                                 ToolResult
}

func (r ToolExecutionReceipt) Clone() ToolExecutionReceipt { r.Result = r.Result.Clone(); return r }
func (r ToolExecutionReceipt) Validate() error {
	if err := r.SemanticMeta.Validate(); err != nil {
		return err
	}
	if err := r.Invocation.Validate(); err != nil {
		return err
	}
	if r.Invocation.SessionID != r.SessionID || !semanticID(r.Method) || !semanticID(r.MutationReceiptID) || !ValidHash(r.RequestHash) {
		return invalid("tool receipt: invocation/request mismatch")
	}
	return r.Result.Validate()
}
