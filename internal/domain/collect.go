package domain

import "slices"

type CollectScope string

const (
	CollectSession CollectScope = "SESSION"
	CollectTask    CollectScope = "TASK"
)

type GCTrigger string

const (
	GCManual         GCTrigger = "MANUAL"
	GCTaskCompletion GCTrigger = "TASK_COMPLETION"
	GCSupersession   GCTrigger = "SUPERSESSION"
	GCTTL            GCTrigger = "TTL"
	GCPolicy         GCTrigger = "POLICY"
)

type CollectIntent struct {
	RequestID string
	Scope     CollectScope
	TaskID    string
	Trigger   GCTrigger
}

func (i CollectIntent) Validate() error {
	if !semanticID(i.RequestID) || i.Scope != CollectSession && i.Scope != CollectTask || i.Scope == CollectTask && !semanticID(i.TaskID) || i.Scope == CollectSession && i.TaskID != "" {
		return invalid("collect intent: explicit collection scope required")
	}
	switch i.Trigger {
	case GCManual, GCTaskCompletion, GCSupersession, GCTTL, GCPolicy:
		return nil
	}
	return invalid("collect intent: unknown trigger")
}

type GCRequest struct {
	SemanticMeta
	CollectIntent
	Origin        Principal
	PolicyVersion string
}

func (r GCRequest) Validate() error {
	if err := r.SemanticMeta.Validate(); err != nil {
		return err
	}
	if err := r.CollectIntent.Validate(); err != nil {
		return err
	}
	if !semanticID(r.PolicyVersion) {
		return invalid("GC request: policy required")
	}
	return semanticActor(r.SessionID, r.Origin)
}

type ItemRevisionRef struct {
	ItemID  string
	Version uint64
}

func (r ItemRevisionRef) Validate() error {
	if !semanticID(r.ItemID) || r.Version == 0 {
		return invalid("item revision: exact occurrence and version required")
	}
	return nil
}

type GCDecisionCode string

const (
	GCArchive    GCDecisionCode = "ARCHIVE"
	GCProtected  GCDecisionCode = "PROTECTED"
	GCIneligible GCDecisionCode = "INELIGIBLE"
)

type GCDecision struct {
	Target ItemRevisionRef
	Code   GCDecisionCode
}
type CollectReceipt struct {
	SemanticMeta
	RequestID, GCRequestID, PolicyVersion string
	Principal                             Principal
	SnapshotSeq                           uint64
	CandidateRefs                         []ItemRevisionRef // frozen deterministic (Seq, ID) order
	Decisions                             []GCDecision
	ArchivedRefs                          []ItemRevisionRef
}

func (r CollectReceipt) Clone() CollectReceipt {
	r.CandidateRefs = slices.Clone(r.CandidateRefs)
	r.Decisions = slices.Clone(r.Decisions)
	r.ArchivedRefs = slices.Clone(r.ArchivedRefs)
	return r
}
func (r CollectReceipt) Validate() error {
	if err := r.SemanticMeta.Validate(); err != nil {
		return err
	}
	if !semanticID(r.RequestID) || !semanticID(r.PolicyVersion) || r.SnapshotSeq > r.Seq || len(r.Decisions) != len(r.CandidateRefs) {
		return invalid("collect receipt: incomplete frozen result")
	}
	if err := semanticActor(r.SessionID, r.Principal); err != nil {
		return err
	}
	archived := 0
	seen := map[string]bool{}
	for n, ref := range r.CandidateRefs {
		if err := ref.Validate(); err != nil {
			return err
		}
		if seen[ref.ItemID] || r.Decisions[n].Target != ref {
			return invalid("collect receipt: duplicate or mismatched candidate")
		}
		seen[ref.ItemID] = true
		switch r.Decisions[n].Code {
		case GCArchive:
			if archived >= len(r.ArchivedRefs) || r.ArchivedRefs[archived].ItemID != ref.ItemID || ref.Version == ^uint64(0) || r.ArchivedRefs[archived].Version != ref.Version+1 {
				return invalid("collect receipt: archived results disagree")
			}
			archived++
		case GCProtected, GCIneligible:
		default:
			return invalid("collect receipt: unknown decision")
		}
	}
	if archived != len(r.ArchivedRefs) {
		return invalid("collect receipt: extra archived results")
	}
	return nil
}

type GCResult struct {
	SemanticMeta
	GCRequestID, CollectReceiptID string
}

func (r GCResult) Validate() error {
	if err := r.SemanticMeta.Validate(); err != nil {
		return err
	}
	if !semanticID(r.GCRequestID) || !semanticID(r.CollectReceiptID) {
		return invalid("GC result: request and effect receipt required")
	}
	return nil
}
