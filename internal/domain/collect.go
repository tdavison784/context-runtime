package domain

import (
	"slices"
	"strconv"
)

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

func (t GCTrigger) Valid() bool {
	switch t {
	case GCManual, GCTaskCompletion, GCSupersession, GCTTL, GCPolicy:
		return true
	}
	return false
}

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
	if !i.Trigger.Valid() {
		return invalid("collect intent: unknown trigger")
	}
	return nil
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

// GCOutcome is a GC request's terminal outcome (H3). A request has at most
// one GCResult; a FAILED request is quarantined and never retried
// automatically; re-arming requires a new request identity.
type GCOutcome string

const (
	GCCollected GCOutcome = "COLLECTED"
	GCFailed    GCOutcome = "FAILED"
)

// GCFailureCode is the closed reason set of a FAILED GC request (H3).
type GCFailureCode string

const (
	GCFailurePolicyMismatch    GCFailureCode = "POLICY_MISMATCH"
	GCFailureInvalidRequest    GCFailureCode = "INVALID_REQUEST"
	GCFailureIntegrity         GCFailureCode = "INTEGRITY"
	GCFailureAttemptsExhausted GCFailureCode = "ATTEMPTS_EXHAUSTED"
)

func (c GCFailureCode) Valid() bool {
	switch c {
	case GCFailurePolicyMismatch, GCFailureInvalidRequest, GCFailureIntegrity, GCFailureAttemptsExhausted:
		return true
	}
	return false
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

// GCCursor is a position in (Seq, ID) candidate order; the zero cursor
// precedes every candidate.
type GCCursor struct {
	Seq uint64
	ID  string
}

// GCProgress is operational claim metadata for one GC request processed in
// bounded batches across passes (H3, P3-39): the durable candidate cursor,
// completed batches and attempts. It is CAS-written on Revision and is never
// a substitute for a batch's CollectReceipt or the request's GCResult.
type GCProgress struct {
	SessionID, GCRequestID      string
	Cursor                      GCCursor
	Batches, Attempts, Revision uint64
}

func (p GCProgress) Clone() GCProgress { return p }

func (p GCProgress) Validate() error {
	if !semanticID(p.SessionID) || !semanticID(p.GCRequestID) || p.Revision == 0 {
		return invalid("GC progress: session, request and revision required")
	}
	if p.Cursor.Seq == 0 && p.Cursor.ID != "" || p.Cursor.Seq != 0 && !semanticID(p.Cursor.ID) {
		return invalid("GC progress: invalid cursor")
	}
	if p.Batches == 0 && p.Cursor != (GCCursor{}) {
		return invalid("GC progress: a cursor advances only with a completed batch")
	}
	return nil
}

// GCBatchRequestID is the CollectReceipt request ID of batch n (from 1) of a
// GC request: its collection request ID (GCRequest.RequestID, the reserved
// "gc_" value, not the "gcq_" record ID) + "/batch/" + n. It inherits that
// reserved runtime namespace, so no caller can name it.
func GCBatchRequestID(gcRequestID string, batch uint64) (string, error) {
	id := gcRequestID + "/batch/" + strconv.FormatUint(batch, 10)
	if !semanticID(gcRequestID) || batch == 0 || !semanticID(id) {
		return "", invalid("GC batch request: request and batch number required")
	}
	return id, nil
}

const (
	gcTriggerRequestPrefix = "gc_"
	gcRequestRecordPrefix  = "gcq_"
)

// GCTriggerRequestID is the runtime collection request ID of a GC trigger
// (H5, SEC-2.6): it binds the authenticated principal whose action raised
// the trigger (a task completion or supersession) and the trigger identity.
// "gc_" is a reserved runtime namespace, so no caller can name or squat it.
func GCTriggerRequestID(origin Principal, trigger GCTrigger, triggerID string) (string, error) {
	if err := validateIngestPrincipal(origin); err != nil {
		return "", err
	}
	if !trigger.Valid() || !semanticID(triggerID) {
		return "", invalid("GC trigger request: trigger and trigger identity required")
	}
	e := NewCanonicalEncoder("context-runtime/gc-trigger/v2")
	encodePrincipal(e, origin)
	return gcTriggerRequestPrefix + e.String(string(trigger)).String(triggerID).Hash(), nil
}

// GCRequestRecordID is the record ID of the GC request whose collection
// request ID is requestID, in the reserved "gcq_" namespace.
func GCRequestRecordID(session, requestID string) (string, error) {
	if !semanticID(session) || !semanticID(requestID) {
		return "", invalid("GC request identity: session and request required")
	}
	return gcRequestRecordPrefix + NewCanonicalEncoder("context-runtime/gc-request/v1").String(session).String(requestID).Hash(), nil
}
