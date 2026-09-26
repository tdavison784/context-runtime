package sqlite

import (
	"cmp"
	"errors"
	"fmt"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// An ingestion is stored as typed rows (D14, D16, D1): the envelope, a
// receipt row, one receipt_item row per original item value (an immutable
// snapshot; the item itself may change later), and the diagnostic and
// command records, which the receipt row references by ID in order.

// receiptRow is domain.IngestReceipt without its nested record lists.
// TestReceiptRowCoversReceipt keeps the two in step.
type receiptRow struct {
	SessionID     string
	OccurrenceID  string
	EventID       string
	Principal     domain.Principal
	PayloadHash   string
	Seq           uint64
	OpenedTurn    uint64
	TurnID        string
	ItemCount     int
	DiagnosticIDs []string
	CommandIDs    []string
	Duplicates    []domain.IngestLink
	Replacements  []domain.IngestLink
	Versions      domain.ExecutionVersions
	SchemaVersion string
	// Phase 3 receipt fields (P3-40); empty on a legacy receipt.
	RequestHashVersion string
	MutationReceiptIDs []string
	Operations         []domain.OperationResult
}

// receiptItem is the original value of the Ordinal-th item a receipt
// records.
type receiptItem struct {
	SessionID    string
	OccurrenceID string
	Ordinal      int
	Item         domain.ContextItem
}

func (t *transaction) InsertIngestion(env domain.EventEnvelope, r domain.IngestReceipt) error {
	if err := store.ValidateIngestion(t.session, env, r); err != nil {
		return err
	}
	var old receiptRow
	switch err := t.get("receipt", r.OccurrenceID, 0, &old); {
	case err == nil:
		if old.PayloadHash != r.PayloadHash {
			return fmt.Errorf("occurrence %s: %w", r.OccurrenceID, domain.ErrEventIDConflict)
		}
		return fmt.Errorf("%w: receipt %s", domain.ErrImmutable, r.OccurrenceID)
	case !errors.Is(err, domain.ErrNotFound):
		return err
	}
	if err := t.checkSeq(r.Seq); err != nil {
		return err
	}
	for _, snap := range r.Items {
		stored, err := t.Item(snap.ID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if err != nil || !t.allocated[snap.Seq] || !store.ReceiptItemMatches(stored, snap) {
			return fmt.Errorf("%w: receipt %s: item %s is not the item this transaction stored", domain.ErrInvalidRecord, r.OccurrenceID, snap.ID)
		}
	}
	for _, links := range [][]domain.IngestLink{r.Duplicates, r.Replacements} {
		for _, l := range links {
			if err := t.requireItem(l.TargetID, 0); err != nil {
				return err
			}
		}
	}
	for _, c := range r.Lifecycle {
		if c.Resolution == domain.TargetResolved {
			if err := t.requireItem(c.ResolvedItemID, c.ResolvedVersion); err != nil {
				return err
			}
		}
	}
	if err := t.checkEnvelopeBlobs(env); err != nil {
		return err
	}
	row := receiptRow{SessionID: r.SessionID, OccurrenceID: r.OccurrenceID, EventID: r.EventID, Principal: r.Principal,
		PayloadHash: r.PayloadHash, Seq: r.Seq, OpenedTurn: r.OpenedTurn, TurnID: r.TurnID, ItemCount: len(r.Items),
		DiagnosticIDs: []string{}, CommandIDs: []string{}, Duplicates: r.Duplicates, Replacements: r.Replacements,
		Versions: r.Versions, SchemaVersion: r.SchemaVersion,
		RequestHashVersion: r.RequestHashVersion, MutationReceiptIDs: r.MutationReceiptIDs, Operations: r.Operations}
	for _, d := range r.Diagnostics {
		row.DiagnosticIDs = append(row.DiagnosticIDs, d.ID)
	}
	for _, c := range r.Lifecycle {
		row.CommandIDs = append(row.CommandIDs, c.ID)
	}
	return t.atomic(func() error {
		if err := t.put("envelope", env.OccurrenceID, 0, env, false); err != nil {
			return err
		}
		if err := t.put("receipt", r.OccurrenceID, 0, row, false); err != nil {
			return err
		}
		for i, it := range r.Items {
			if err := t.put("receipt_item", r.OccurrenceID, i, receiptItem{SessionID: t.session, OccurrenceID: r.OccurrenceID, Ordinal: i, Item: it}, false); err != nil {
				return err
			}
		}
		for _, d := range r.Diagnostics {
			if err := t.put("diagnostic", d.ID, 0, d, false); err != nil {
				return err
			}
		}
		for _, c := range r.Lifecycle {
			if err := t.put("command", c.ID, 0, c, false); err != nil {
				return err
			}
		}
		return nil
	})
}

// requireItem fails with domain.ErrInvalidRecord unless itemID names a
// stored item of at least version minVersion.
func (t *transaction) requireItem(itemID string, minVersion uint64) error {
	it, err := t.Item(itemID)
	if errors.Is(err, domain.ErrNotFound) || err == nil && it.Version < minVersion {
		return fmt.Errorf("%w: receipt names item %s, which is not stored", domain.ErrInvalidRecord, itemID)
	}
	return err
}

// checkEnvelopeBlobs verifies every blob the envelope references is stored
// in this session with the referenced size (FR-ING-007).
func (t *transaction) checkEnvelopeBlobs(env domain.EventEnvelope) error {
	for _, span := range env.Event.Spans {
		for _, p := range span.Parts {
			if p.BlobHash == "" {
				continue
			}
			b, err := t.Blob(p.BlobHash)
			if errors.Is(err, domain.ErrNotFound) || err == nil && uint64(len(b.Data)) != p.BlobSize {
				return fmt.Errorf("%w: envelope references blob %s, which is not stored", domain.ErrIntegrity, p.BlobHash)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *transaction) Receipt(occurrenceID string) (domain.IngestReceipt, error) {
	var row receiptRow
	if err := t.get("receipt", occurrenceID, 0, &row); err != nil {
		return domain.IngestReceipt{}, err
	}
	r := domain.IngestReceipt{SessionID: row.SessionID, OccurrenceID: row.OccurrenceID, EventID: row.EventID, Principal: row.Principal,
		PayloadHash: row.PayloadHash, Seq: row.Seq, OpenedTurn: row.OpenedTurn, TurnID: row.TurnID, Duplicates: row.Duplicates,
		Replacements: row.Replacements, Versions: row.Versions, SchemaVersion: row.SchemaVersion,
		RequestHashVersion: row.RequestHashVersion, MutationReceiptIDs: row.MutationReceiptIDs, Operations: row.Operations,
		Items: make([]domain.ContextItem, row.ItemCount)}
	for i := range r.Items {
		var ri receiptItem
		if err := t.get("receipt_item", occurrenceID, i, &ri); err != nil {
			return domain.IngestReceipt{}, integrityIfMissing(err, "receipt item")
		}
		if err := verifyItemContent(ri.Item); err != nil {
			return domain.IngestReceipt{}, err
		}
		r.Items[i] = ri.Item
	}
	for _, id := range row.DiagnosticIDs {
		var d domain.DiagnosticRecord
		if err := t.get("diagnostic", id, 0, &d); err != nil {
			return domain.IngestReceipt{}, integrityIfMissing(err, "diagnostic")
		}
		r.Diagnostics = append(r.Diagnostics, d)
	}
	for _, id := range row.CommandIDs {
		var c domain.LifecycleCommandRecord
		if err := t.get("command", id, 0, &c); err != nil {
			return domain.IngestReceipt{}, integrityIfMissing(err, "lifecycle command")
		}
		r.Lifecycle = append(r.Lifecycle, c)
	}
	return r, nil
}

// integrityIfMissing reports a record a receipt references but the store
// lacks as corruption, not absence.
func integrityIfMissing(err error, what string) error {
	if errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("%w: receipt references a missing %s", domain.ErrIntegrity, what)
	}
	return err
}

func (t *transaction) Envelope(occurrenceID string) (domain.EventEnvelope, error) {
	var v domain.EventEnvelope
	err := t.get("envelope", occurrenceID, 0, &v)
	return v, err
}

// visibleReceipts returns the receipts an occurrence filter selects, ordered
// by Seq, after validating the viewer.
func (t *transaction) visibleReceipts(viewer domain.Principal, occurrenceID string) ([]domain.IngestReceipt, error) {
	if err := viewer.Validate(); err != nil {
		return nil, err
	}
	var occs []string
	if occurrenceID != "" {
		occs = []string{occurrenceID}
	} else {
		rows, err := listRecords[receiptRow](t, "receipt")
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			occs = append(occs, row.OccurrenceID)
		}
	}
	var out []domain.IngestReceipt
	for _, occ := range occs {
		r, err := t.Receipt(occ)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b domain.IngestReceipt) int { return cmp.Compare(a.Seq, b.Seq) })
	return out, nil
}

func (t *transaction) Diagnostics(f store.DiagnosticFilter) ([]domain.DiagnosticRecord, error) {
	recs, err := t.visibleReceipts(f.Viewer, f.OccurrenceID)
	if err != nil {
		return nil, err
	}
	out := []domain.DiagnosticRecord{}
	for _, r := range recs {
		for _, d := range r.Diagnostics {
			if d.VisibleTo(f.Viewer) {
				out = append(out, d)
			}
		}
	}
	return out, nil
}

func (t *transaction) LifecycleCommands(f store.CommandFilter) ([]domain.LifecycleCommandRecord, error) {
	recs, err := t.visibleReceipts(f.Viewer, f.OccurrenceID)
	if err != nil {
		return nil, err
	}
	out := []domain.LifecycleCommandRecord{}
	for _, r := range recs {
		for _, c := range r.Lifecycle {
			if c.Access.Permits(f.Viewer) {
				// The record is visible at its transcript boundary; its
				// resolution only where DetailAccess permits (SEC-3.2).
				out = append(out, c.Redacted(f.Viewer))
			}
		}
	}
	return out, nil
}
