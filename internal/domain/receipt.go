package domain

import (
	"fmt"
	"slices"
)

// IngestLink reports a newly created item's duplicate/replacement target.
type IngestLink struct{ ItemID, TargetID string }

func (l IngestLink) Validate() error {
	if l.ItemID == "" || l.TargetID == "" || l.ItemID == l.TargetID {
		return invalid("ingest link: distinct item and target IDs required")
	}
	return nil
}

// ExecutionVersions are the runtime-selected behavior versions an event was
// processed under (D14, M2). They are persisted execution inputs, never part
// of the caller's payload fingerprint, so a retry after an upgrade still
// matches and returns the original receipt. Limits are the effective values.
type ExecutionVersions struct {
	Parser   string
	Policy   string
	Limits   Limits
	Semantic *Phase3Policy
}

// Validate checks that every version is recorded and limits are effective.
func (v ExecutionVersions) Validate() error {
	if v.Semantic != nil {
		if err := v.Semantic.Validate(); err != nil {
			return err
		}
	}
	if v.Parser == "" || v.Policy == "" {
		return invalid("execution versions: parser and policy versions are required")
	}
	if err := v.Limits.Validate(); err != nil {
		return err
	}
	if v.Limits != v.Limits.Effective() {
		return invalid("execution versions: limits must be recorded as effective values")
	}
	return nil
}

// EventEnvelopeSchemaVersion versions the persisted request envelope.
const EventEnvelopeSchemaVersion = "event-envelope/v1"
const EventEnvelopeSchemaV2 = "event-envelope/v2"

// EventEnvelope is the replayable immutable request (D14): the complete
// authenticated event as accepted, with image/document bytes replaced by
// their verified blob references (the bytes live in session blobs). Replay
// reads the envelope; it never dereferences locators.
type EventEnvelope struct {
	RequestHashVersion string
	SemanticPolicy     *Phase3Policy
	Limits             Limits
	SessionID          string
	OccurrenceID       string
	EventID            string
	Principal          Principal
	Event              Event
	PayloadHash        string
	SchemaVersion      string
}

// NewEventEnvelope snapshots e for principal p under occurrenceID. It deep
// copies the event, so later caller mutation cannot change it.
func NewEventEnvelope(p Principal, occurrenceID string, e Event) (EventEnvelope, error) {
	hash, err := e.PayloadHash(p)
	if err != nil {
		return EventEnvelope{}, err
	}
	e = snapshotEventBlobs(e)
	env := EventEnvelope{SessionID: p.SessionID, OccurrenceID: occurrenceID, EventID: e.EventID, Principal: p, Event: e, PayloadHash: hash, SchemaVersion: EventEnvelopeSchemaVersion}
	return env, env.Validate()
}

func snapshotEventBlobs(e Event) Event {
	e = e.Clone()
	for i := range e.Spans {
		for j, part := range e.Spans[i].Parts {
			if part.Data != nil {
				snap := part.Snapshot()
				e.Spans[i].Parts[j] = InputPart{Type: part.Type, MediaType: part.MediaType, BlobHash: snap.BlobHash, BlobSize: snap.BlobSize}
			}
		}
	}
	return e
}

// Validate recomputes the payload hash and checks the occurrence key.
func (v EventEnvelope) Validate() error {
	if (v.SchemaVersion != EventEnvelopeSchemaVersion && v.SchemaVersion != EventEnvelopeSchemaV2) || v.SessionID == "" || v.Principal.SessionID != v.SessionID || v.Event.EventID != v.EventID {
		return invalid("event envelope: schema, session, principal, or event ID mismatch")
	}
	if !OccurrenceMatchesEvent(v.SessionID, v.OccurrenceID, v.EventID) {
		return invalid("event envelope: occurrence disagrees with event ID")
	}
	for _, s := range v.Event.Spans {
		for _, part := range s.Parts {
			if part.Data != nil {
				return invalid("event envelope: blob bytes must be stored as references")
			}
		}
	}
	var hash string
	var err error
	if v.SchemaVersion == EventEnvelopeSchemaVersion {
		if v.RequestHashVersion != "" && v.RequestHashVersion != RequestHashV2 || v.SemanticPolicy != nil || v.Limits != (Limits{}) {
			return invalid("legacy envelope: unexpected semantic metadata")
		}
		hash, err = v.Event.PayloadHash(v.Principal)
	} else {
		if v.RequestHashVersion != RequestHashV3 || v.SemanticPolicy == nil {
			return invalid("semantic envelope: recorded hash schema and policy required")
		}
		hash, err = v.Event.PayloadHashFor(v.RequestHashVersion, v.Principal, v.Limits, *v.SemanticPolicy)
	}
	if err != nil {
		return err
	}
	if hash != v.PayloadHash {
		return ErrIntegrity
	}
	return nil
}

// Clone returns a deep copy.
func (v EventEnvelope) Clone() EventEnvelope {
	v.Event = v.Event.Clone()
	if v.SemanticPolicy != nil {
		p := v.SemanticPolicy.Clone()
		v.SemanticPolicy = &p
	}
	return v
}

// IngestReceiptSchemaVersion versions the persisted receipt.
const IngestReceiptSchemaVersion = "ingest-receipt/v1"
const IngestReceiptSchemaV2 = "ingest-receipt/v2"

// IngestReceipt is the immutable original result of one accepted event
// (D14), persisted atomically with its effects. An idempotent retry returns
// it unchanged, without reparsing, re-resolving, or reading mutable item
// state: Items holds the values as created, even after later lifecycle
// changes. It is internal (R3): the root package does not alias it.
// OpenedTurn/TurnID are set only when the event opened a turn (D18).
type IngestReceipt struct {
	RequestHashVersion string
	MutationReceiptIDs []string
	Operations         []OperationResult
	SessionID          string
	OccurrenceID       string
	EventID            string
	Principal          Principal
	PayloadHash        string
	Seq                uint64
	OpenedTurn         uint64
	TurnID             string
	Items              []ContextItem // creation order
	Diagnostics        []DiagnosticRecord
	Lifecycle          []LifecycleCommandRecord
	Duplicates         []IngestLink
	Replacements       []IngestLink
	Versions           ExecutionVersions
	SchemaVersion      string
}

// ItemIDs returns the created item IDs in creation order.
func (r IngestReceipt) ItemIDs() []string {
	out := make([]string, len(r.Items))
	for i, it := range r.Items {
		out[i] = it.ID
	}
	return out
}

// Clone returns a deep copy.
func (r IngestReceipt) Clone() IngestReceipt {
	r.MutationReceiptIDs = slices.Clone(r.MutationReceiptIDs)
	r.Operations = slices.Clone(r.Operations)
	for i := range r.Operations {
		r.Operations[i] = r.Operations[i].Clone()
	}
	if r.Versions.Semantic != nil {
		p := r.Versions.Semantic.Clone()
		r.Versions.Semantic = &p
	}
	items := make([]ContextItem, len(r.Items))
	for i, it := range r.Items {
		items[i] = it.Clone()
	}
	r.Items = items
	r.Diagnostics = slices.Clone(r.Diagnostics)
	r.Lifecycle = slices.Clone(r.Lifecycle)
	for i := range r.Lifecycle {
		r.Lifecycle[i] = r.Lifecycle[i].Clone()
	}
	r.Duplicates = slices.Clone(r.Duplicates)
	r.Replacements = slices.Clone(r.Replacements)
	return r
}

// Validate checks internal consistency: every artifact belongs to this
// occurrence, diagnostics are in stable (span, index) order, commands are in
// ordinal order, and links name items the event created.
func (r IngestReceipt) Validate() error {
	if r.SchemaVersion == IngestReceiptSchemaVersion {
		if r.RequestHashVersion != "" && r.RequestHashVersion != RequestHashV2 || r.Versions.Semantic != nil || r.MutationReceiptIDs != nil || r.Operations != nil {
			return invalid("legacy receipt: unexpected semantic metadata")
		}
	} else if r.RequestHashVersion != RequestHashV3 || r.Versions.Semantic == nil {
		return invalid("semantic receipt: recorded hash schema and policy required")
	}
	if (r.SchemaVersion != IngestReceiptSchemaVersion && r.SchemaVersion != IngestReceiptSchemaV2) || r.SessionID == "" || r.Seq == 0 || !ValidHash(r.PayloadHash) {
		return invalid("ingest receipt: schema, session, sequence, and payload hash are required")
	}
	if !OccurrenceMatchesEvent(r.SessionID, r.OccurrenceID, r.EventID) {
		return invalid("ingest receipt: occurrence disagrees with event ID")
	}
	if err := r.Principal.Validate(); err != nil {
		return err
	}
	if r.Principal.SessionID != r.SessionID {
		return invalid("ingest receipt: principal belongs to another session")
	}
	if (r.OpenedTurn == 0) != (r.TurnID == "") ||
		r.OpenedTurn != 0 && (r.Principal.TaskID == "" || r.TurnID != DerivedTurnID(r.SessionID, r.Principal.TaskID, r.OpenedTurn)) {
		return invalid("ingest receipt: opened turn disagrees with its turn ID")
	}
	if err := r.Versions.Validate(); err != nil {
		return err
	}
	for i, result := range r.Operations {
		if err := result.Validate(); err != nil {
			return err
		}
		if result.Index != i || result.Access.SessionID != r.SessionID {
			return invalid("receipt: invalid operation order/session")
		}
	}
	ids := map[string]bool{}
	for _, it := range r.Items {
		if err := it.Validate(); err != nil {
			return err
		}
		if it.SessionID != r.SessionID || it.EventID != r.EventID || ids[it.ID] {
			return invalid("ingest receipt: item %s is foreign or repeated", it.ID)
		}
		ids[it.ID] = true
	}
	for i, d := range r.Diagnostics {
		if err := d.Validate(); err != nil {
			return err
		}
		if d.SessionID != r.SessionID || d.OccurrenceID != r.OccurrenceID || d.EventID != r.EventID {
			return invalid("ingest receipt: foreign diagnostic")
		}
		if i > 0 {
			p := r.Diagnostics[i-1]
			if p.SpanIndex > d.SpanIndex || p.SpanIndex == d.SpanIndex && p.Index >= d.Index {
				return invalid("ingest receipt: diagnostics out of order")
			}
		}
	}
	for i, c := range r.Lifecycle {
		if err := c.Validate(); err != nil {
			return err
		}
		if c.SessionID != r.SessionID || c.OccurrenceID != r.OccurrenceID || c.EventID != r.EventID || c.Ordinal != i {
			return invalid("ingest receipt: foreign or misordered lifecycle command")
		}
	}
	for _, links := range [][]IngestLink{r.Duplicates, r.Replacements} {
		for _, link := range links {
			if err := link.Validate(); err != nil {
				return fmt.Errorf("ingest receipt: %w", err)
			}
			if !ids[link.ItemID] {
				return invalid("ingest receipt: link from an item the event did not create")
			}
		}
	}
	return nil
}
