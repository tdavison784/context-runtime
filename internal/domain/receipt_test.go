package domain

import (
	"errors"
	"testing"
)

func receiptFixture() IngestReceipt {
	p := Principal{SessionID: "s1", TaskID: "t1", AgentID: "a1", Authority: AuthorityUser}
	occ := CallerOccurrenceID("s1", "e1")
	it := validItem()
	it.EventID = "e1"
	it2 := it.Clone()
	it2.ID = "itm_2"
	d := diagnosticRecordFixture()
	d2 := d
	d2.SpanIndex, d2.Index = 2, 0
	d2.ID = DiagnosticRecordID("s1", occ, 2, 0)
	return IngestReceipt{
		SessionID: "s1", OccurrenceID: occ, EventID: "e1", Principal: p, PayloadHash: HashBytes([]byte("x")), Seq: 7,
		OpenedTurn: 2, TurnID: DerivedTurnID("s1", "t1", 2),
		Items: []ContextItem{it, it2}, Diagnostics: []DiagnosticRecord{d, d2}, Lifecycle: []LifecycleCommandRecord{commandRecordFixture()},
		Duplicates: []IngestLink{{ItemID: "itm_2", TargetID: "itm_old"}},
		Versions:   ExecutionVersions{Parser: "directive/v1", Policy: "policy/v1", Limits: DefaultLimits()}, SchemaVersion: IngestReceiptSchemaVersion,
	}
}

func TestIngestReceiptValidate(t *testing.T) {
	r := receiptFixture()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if ids := r.ItemIDs(); len(ids) != 2 || ids[0] != "itm_1" || ids[1] != "itm_2" {
		t.Fatalf("ItemIDs = %v", ids)
	}
	for name, mut := range map[string]func(*IngestReceipt){
		"schema":           func(r *IngestReceipt) { r.SchemaVersion = "" },
		"seq":              func(r *IngestReceipt) { r.Seq = 0 },
		"hash":             func(r *IngestReceipt) { r.PayloadHash = "x" },
		"occurrence":       func(r *IngestReceipt) { r.EventID = "e2" },
		"principal":        func(r *IngestReceipt) { r.Principal.SessionID = "s2" },
		"turn id":          func(r *IngestReceipt) { r.TurnID = "turn_x" },
		"turn w/o id":      func(r *IngestReceipt) { r.TurnID = "" },
		"id w/o turn":      func(r *IngestReceipt) { r.OpenedTurn = 0 },
		"versions":         func(r *IngestReceipt) { r.Versions.Policy = "" },
		"raw limits":       func(r *IngestReceipt) { r.Versions.Limits = Limits{} },
		"repeated item":    func(r *IngestReceipt) { r.Items[1].ID = "itm_1" },
		"foreign item":     func(r *IngestReceipt) { r.Items[0].EventID = "e2" },
		"diag order":       func(r *IngestReceipt) { r.Diagnostics[0], r.Diagnostics[1] = r.Diagnostics[1], r.Diagnostics[0] },
		"diag repeated":    func(r *IngestReceipt) { r.Diagnostics[1] = r.Diagnostics[0] },
		"foreign diag":     func(r *IngestReceipt) { r.Diagnostics = []DiagnosticRecord{anonymousDiagnostic()} },
		"command ordinal":  func(r *IngestReceipt) { r.Lifecycle = append(r.Lifecycle, r.Lifecycle[0]) },
		"link from other":  func(r *IngestReceipt) { r.Replacements = []IngestLink{{ItemID: "itm_old", TargetID: "itm_1"}} },
		"self link":        func(r *IngestReceipt) { r.Duplicates = []IngestLink{{ItemID: "itm_1", TargetID: "itm_1"}} },
		"invalid item":     func(r *IngestReceipt) { r.Items[0].Version = 0 },
		"invalid command":  func(r *IngestReceipt) { r.Lifecycle[0].Status = "EXECUTED" },
		"invalid diagnose": func(r *IngestReceipt) { r.Diagnostics[0].SchemaVersion = "" },
	} {
		c := r.Clone()
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s: invalid receipt accepted", name)
		}
	}
}

func anonymousDiagnostic() DiagnosticRecord {
	d := diagnosticRecordFixture()
	var gen SequentialIDs
	d.OccurrenceID, d.EventID = NewAnonymousOccurrenceID(&gen), ""
	d.ID = DiagnosticRecordID("s1", d.OccurrenceID, d.SpanIndex, d.Index)
	return d
}

func TestIngestReceiptCloneIsDeep(t *testing.T) {
	r := receiptFixture()
	c := r.Clone()
	c.Items[0].Parts[0].Text = "changed"
	c.Diagnostics[0].Index = 99
	c.Lifecycle[0].TargetID = "x"
	c.Duplicates[0].TargetID = "x"
	if r.Items[0].Parts[0].Text != "hi" || r.Diagnostics[0].Index != 3 || r.Lifecycle[0].TargetID != "architecture" || r.Duplicates[0].TargetID != "itm_old" {
		t.Fatal("Clone shares receipt state")
	}
}

func TestEventEnvelope(t *testing.T) {
	p, e := ingestFixture()
	data := []byte("pdf-bytes")
	e.Spans = append(e.Spans, Span{Authority: AuthorityRetrievedContent, Access: e.Spans[0].Access, Parts: []InputPart{{Type: PartDocument, MediaType: "application/pdf", Data: data}}})
	env, err := NewEventEnvelope(p, CallerOccurrenceID(p.SessionID, e.EventID), e)
	if err != nil {
		t.Fatal(err)
	}
	part := env.Event.Spans[1].Parts[0]
	if part.Data != nil || part.BlobHash != HashBytes(data) || part.BlobSize != uint64(len(data)) {
		t.Fatalf("envelope kept bytes or lost reference: %+v", part)
	}
	want, _ := e.PayloadHash(p)
	if env.PayloadHash != want {
		t.Fatal("envelope changed the payload identity")
	}
	e.Spans[0].Parts[0].Text = "mutated"
	if env.Event.Spans[0].Parts[0].Text == "mutated" {
		t.Fatal("envelope shares caller buffers")
	}
	tampered := env.Clone()
	tampered.Event.Spans[0].Parts[0].Text = "tampered"
	if !errors.Is(tampered.Validate(), ErrIntegrity) {
		t.Fatal("tampered envelope accepted")
	}
	if _, err := NewEventEnvelope(p, "evc_wrong", e); err == nil {
		t.Fatal("mismatched occurrence accepted")
	}
	var gen SequentialIDs
	e.EventID = ""
	if _, err := NewEventEnvelope(p, NewAnonymousOccurrenceID(&gen), e); err != nil {
		t.Fatalf("anonymous envelope rejected: %v", err)
	}
	if _, err := NewEventEnvelope(p, CallerOccurrenceID(p.SessionID, "x"), e); err == nil {
		t.Fatal("anonymous event under a caller occurrence accepted")
	}
}
