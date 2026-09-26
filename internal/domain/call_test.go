package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestCallStateValid(t *testing.T) {
	for _, s := range []CallState{CallPrepared, CallSent, CallCompleted, CallFailed, CallUnknown, CallAbandoned} {
		if !s.Valid() {
			t.Errorf("CallState(%q).Valid() = false, want true", s)
		}
	}
	if CallState("bogus").Valid() {
		t.Error("CallState(bogus).Valid() = true, want false")
	}
}

func TestOperationKindValid(t *testing.T) {
	if !OperationInference.Valid() || !OperationCompaction.Valid() {
		t.Error("known operation kinds must be valid")
	}
	if OperationKind("bogus").Valid() {
		t.Error("OperationKind(bogus).Valid() = true, want false")
	}
}

// TestValidCallTransitionMatrix exhaustively checks every (from, to) pair
// among the six call states against the table in call.go: only nine
// transitions are allowed, and SENT/UNKNOWN never go directly back to SENT
// (FR-CALL-002: no automatic resend).
func TestValidCallTransitionMatrix(t *testing.T) {
	allowed := map[[2]CallState]bool{
		{CallPrepared, CallSent}:     true,
		{CallPrepared, CallFailed}:   true,
		{CallSent, CallCompleted}:    true,
		{CallSent, CallFailed}:       true,
		{CallSent, CallPrepared}:     true,
		{CallSent, CallUnknown}:      true,
		{CallUnknown, CallCompleted}: true,
		{CallUnknown, CallFailed}:    true,
		{CallUnknown, CallAbandoned}: true,
	}
	states := []CallState{CallPrepared, CallSent, CallCompleted, CallFailed, CallUnknown, CallAbandoned}
	for _, from := range states {
		for _, to := range states {
			want := allowed[[2]CallState{from, to}]
			if got := ValidCallTransition(from, to); got != want {
				t.Errorf("ValidCallTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestValidCallTransition_TerminalStatesHaveNoOutgoingTransition(t *testing.T) {
	states := []CallState{CallPrepared, CallSent, CallCompleted, CallFailed, CallUnknown, CallAbandoned}
	for _, from := range []CallState{CallCompleted, CallFailed, CallAbandoned} {
		for _, to := range states {
			if ValidCallTransition(from, to) {
				t.Errorf("ValidCallTransition(%s, %s) = true, want false (terminal state)", from, to)
			}
		}
	}
}

func TestValidCallTransition_NoDirectResendToSent(t *testing.T) {
	if ValidCallTransition(CallSent, CallSent) {
		t.Error("SENT -> SENT must be false: no automatic resend (FR-CALL-002)")
	}
	if ValidCallTransition(CallUnknown, CallSent) {
		t.Error("UNKNOWN -> SENT must be false: no automatic resend (FR-CALL-002)")
	}
}

func TestCallStateReserving(t *testing.T) {
	reserving := map[CallState]bool{
		CallPrepared: true, CallSent: true, CallUnknown: true,
		CallCompleted: false, CallFailed: false, CallAbandoned: false,
	}
	for s, want := range reserving {
		if got := s.Reserving(); got != want {
			t.Errorf("%s.Reserving() = %v, want %v", s, got, want)
		}
	}
}

func TestCallStateTerminal(t *testing.T) {
	terminal := map[CallState]bool{
		CallCompleted: true, CallFailed: true, CallAbandoned: true,
		CallPrepared: false, CallSent: false, CallUnknown: false,
	}
	for s, want := range terminal {
		if got := s.Terminal(); got != want {
			t.Errorf("%s.Terminal() = %v, want %v", s, got, want)
		}
	}
}

func TestCallStateReservingAndTerminalArePartition(t *testing.T) {
	// Every known state is exactly one of Reserving or Terminal, never both,
	// never neither.
	for _, s := range []CallState{CallPrepared, CallSent, CallCompleted, CallFailed, CallUnknown, CallAbandoned} {
		if s.Reserving() == s.Terminal() {
			t.Errorf("%s: Reserving()=%v and Terminal()=%v must differ", s, s.Reserving(), s.Terminal())
		}
	}
}

// --- CallOutcome.OutcomeHash --------------------------------------------------

func TestOutcomeHashStable(t *testing.T) {
	in := int64(5)
	o := CallOutcome{
		State: CallCompleted, ResponseHash: "sha256:" + strings.Repeat("a", 64),
		Usage: []UsageIteration{{Iteration: 1, InputTokens: &in}},
	}
	h1 := o.OutcomeHash()
	h2 := o.OutcomeHash()
	if h1 != h2 {
		t.Error("OutcomeHash is not stable/deterministic for identical outcomes")
	}
	if !ValidHash(h1) {
		t.Fatalf("OutcomeHash produced malformed hash %q", h1)
	}
}

func TestOutcomeHashDistinguishesNilFromZeroUsage(t *testing.T) {
	zero := int64(0)
	nilOutcome := CallOutcome{State: CallCompleted, Usage: []UsageIteration{{Iteration: 1, InputTokens: nil}}}
	zeroOutcome := CallOutcome{State: CallCompleted, Usage: []UsageIteration{{Iteration: 1, InputTokens: &zero}}}
	if nilOutcome.OutcomeHash() == zeroOutcome.OutcomeHash() {
		t.Error("OutcomeHash must distinguish an unknown (nil) usage count from an explicit zero")
	}
}

func TestOutcomeHashDistinguishesEachUsageField(t *testing.T) {
	one := int64(1)
	base := UsageIteration{Iteration: 1}
	fields := []func(*UsageIteration){
		func(u *UsageIteration) { u.InputTokens = &one },
		func(u *UsageIteration) { u.CacheReadTokens = &one },
		func(u *UsageIteration) { u.CacheWriteTokens = &one },
		func(u *UsageIteration) { u.OutputTokens = &one },
		func(u *UsageIteration) { u.ReasoningTokens = &one },
	}
	hashes := map[string]bool{}
	for _, set := range fields {
		u := base
		set(&u)
		h := CallOutcome{State: CallCompleted, Usage: []UsageIteration{u}}.OutcomeHash()
		if hashes[h] {
			t.Fatalf("two different single-field usage settings produced the same hash %q", h)
		}
		hashes[h] = true
	}
}

func TestOutcomeHashDistinguishesStateResponseHashFailureReason(t *testing.T) {
	base := CallOutcome{State: CallCompleted, ResponseHash: "sha256:" + strings.Repeat("a", 64)}
	variants := []CallOutcome{
		base,
		{State: CallFailed, ResponseHash: base.ResponseHash},
		{State: CallCompleted, ResponseHash: "sha256:" + strings.Repeat("b", 64)},
		{State: CallCompleted, ResponseHash: base.ResponseHash, FailureReason: "timeout"},
		{State: CallCompleted, ResponseHash: base.ResponseHash, Retryable: true},
	}
	seen := map[string]bool{}
	for i, v := range variants {
		h := v.OutcomeHash()
		if seen[h] {
			t.Fatalf("variant %d collided with an earlier variant's hash %q", i, h)
		}
		seen[h] = true
	}
}

func TestOutcomeHashDistinguishesUsageLength(t *testing.T) {
	one := CallOutcome{State: CallCompleted, Usage: []UsageIteration{{Iteration: 1}}}
	two := CallOutcome{State: CallCompleted, Usage: []UsageIteration{{Iteration: 1}, {Iteration: 2}}}
	if one.OutcomeHash() == two.OutcomeHash() {
		t.Error("OutcomeHash must distinguish a different number of usage iterations")
	}
}

// TestOutcomeHashDistinguishesAttempt checks OutcomeHash covers Attempt
// (contract v2): an outcome for attempt 1 must never collide with an
// otherwise-identical outcome for attempt 2, so a delayed duplicate transport
// response from an earlier attempt can never be mistaken for, or silently
// treated as a conflict against, a later attempt's outcome.
func TestOutcomeHashDistinguishesAttempt(t *testing.T) {
	base := CallOutcome{Attempt: 1, State: CallCompleted, ResponseHash: "sha256:" + strings.Repeat("a", 64)}
	other := base
	other.Attempt = 2
	if base.OutcomeHash() == other.OutcomeHash() {
		t.Error("OutcomeHash must distinguish different Attempt numbers")
	}
}

// --- CallOutcome.Validate ----------------------------------------------------

func TestCallOutcomeValidate(t *testing.T) {
	response := []byte("response bytes")
	responseHash := HashBytes(response)

	cases := []struct {
		name    string
		o       CallOutcome
		wantErr error
	}{
		{
			"valid completed with response",
			CallOutcome{Attempt: 1, State: CallCompleted, ResponseHash: responseHash, Response: response},
			nil,
		},
		{
			"valid failed with no response",
			CallOutcome{Attempt: 1, State: CallFailed, FailureReason: "timeout", Retryable: true},
			nil,
		},
		{
			"valid failed with a response",
			CallOutcome{Attempt: 1, State: CallFailed, ResponseHash: responseHash, Response: response, FailureReason: "bad status"},
			nil,
		},
		{"attempt zero rejected", CallOutcome{Attempt: 0, State: CallCompleted, ResponseHash: responseHash, Response: response}, ErrInvalidRecord},
		{"attempt negative rejected", CallOutcome{Attempt: -1, State: CallCompleted, ResponseHash: responseHash, Response: response}, ErrInvalidRecord},
		{
			"completed cannot be retryable",
			CallOutcome{Attempt: 1, State: CallCompleted, ResponseHash: responseHash, Response: response, Retryable: true},
			ErrInvalidRecord,
		},
		{
			"completed cannot carry a failure reason",
			CallOutcome{Attempt: 1, State: CallCompleted, ResponseHash: responseHash, Response: response, FailureReason: "oops"},
			ErrInvalidRecord,
		},
		{
			"completed requires a response hash",
			CallOutcome{Attempt: 1, State: CallCompleted},
			ErrInvalidRecord,
		},
		{
			"completed with malformed response hash rejected",
			CallOutcome{Attempt: 1, State: CallCompleted, ResponseHash: "not-a-hash"},
			ErrInvalidRecord,
		},
		{
			"completed with mismatched response bytes is an integrity failure",
			CallOutcome{Attempt: 1, State: CallCompleted, ResponseHash: responseHash, Response: []byte("tampered")},
			ErrIntegrity,
		},
		{
			"failed with malformed response hash rejected",
			CallOutcome{Attempt: 1, State: CallFailed, ResponseHash: "not-a-hash"},
			ErrInvalidRecord,
		},
		{
			"failed with mismatched response bytes is an integrity failure",
			CallOutcome{Attempt: 1, State: CallFailed, ResponseHash: responseHash, Response: []byte("tampered")},
			ErrIntegrity,
		},
		{
			"response bytes without a response hash rejected",
			CallOutcome{Attempt: 1, State: CallFailed, Response: response},
			ErrInvalidRecord,
		},
		{
			"state must be COMPLETED or FAILED: PREPARED rejected",
			CallOutcome{Attempt: 1, State: CallPrepared},
			ErrInvalidRecord,
		},
		{
			"state must be COMPLETED or FAILED: UNKNOWN rejected",
			CallOutcome{Attempt: 1, State: CallUnknown},
			ErrInvalidRecord,
		},
		{
			"state must be COMPLETED or FAILED: invalid state rejected",
			CallOutcome{Attempt: 1, State: "bogus"},
			ErrInvalidRecord,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.o.Validate()
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("Validate() = %v, want error wrapping %v", err, c.wantErr)
			}
		})
	}
}

// --- CallProposalHash ---------------------------------------------------------

// TestCallProposalHashSensitivity checks CallProposalHash changes when any
// one of its documented frozen fields changes (contract v2): session,
// conversation, operation, principal, service actor, base version, semantic
// seq, epoch, policy/descriptor versions, request hash, and manifest hash.
func TestCallProposalHashSensitivity(t *testing.T) {
	base := func() CallRecord {
		return CallRecord{
			SessionID: "s1", ConversationID: "conv1", Operation: OperationInference,
			Principal:               Principal{SessionID: "s1", TaskID: "t1", Authority: AuthorityAgent},
			ServiceActor:            Principal{SessionID: "s1", TaskID: "t1", Authority: AuthorityHarness},
			BaseConversationVersion: 1, SemanticSeq: 1, Epoch: 1,
			PolicyVersion: "p1", DescriptorVersion: "d1",
			RequestHash: "sha256:" + strings.Repeat("a", 64), ManifestHash: "sha256:" + strings.Repeat("b", 64),
		}
	}
	baseHash := CallProposalHash(base())

	mutations := map[string]func(CallRecord) CallRecord{
		"session":                   func(c CallRecord) CallRecord { c.SessionID = "s2"; return c },
		"conversation":              func(c CallRecord) CallRecord { c.ConversationID = "conv2"; return c },
		"operation":                 func(c CallRecord) CallRecord { c.Operation = OperationCompaction; return c },
		"principal session":         func(c CallRecord) CallRecord { c.Principal.SessionID = "s2"; return c },
		"principal task":            func(c CallRecord) CallRecord { c.Principal.TaskID = "t2"; return c },
		"principal workflow":        func(c CallRecord) CallRecord { c.Principal.WorkflowID = "w1"; return c },
		"principal agent":           func(c CallRecord) CallRecord { c.Principal.AgentID = "a1"; return c },
		"principal authority":       func(c CallRecord) CallRecord { c.Principal.Authority = AuthorityUser; return c },
		"service actor session":     func(c CallRecord) CallRecord { c.ServiceActor.SessionID = "s2"; return c },
		"service actor authority":   func(c CallRecord) CallRecord { c.ServiceActor.Authority = AuthoritySystem; return c },
		"base conversation version": func(c CallRecord) CallRecord { c.BaseConversationVersion = 2; return c },
		"semantic seq":              func(c CallRecord) CallRecord { c.SemanticSeq = 2; return c },
		"epoch":                     func(c CallRecord) CallRecord { c.Epoch = 2; return c },
		"policy version":            func(c CallRecord) CallRecord { c.PolicyVersion = "p2"; return c },
		"descriptor version":        func(c CallRecord) CallRecord { c.DescriptorVersion = "d2"; return c },
		"request hash":              func(c CallRecord) CallRecord { c.RequestHash = "sha256:" + strings.Repeat("c", 64); return c },
		"manifest hash":             func(c CallRecord) CallRecord { c.ManifestHash = "sha256:" + strings.Repeat("d", 64); return c },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if got := CallProposalHash(mutate(base())); got == baseHash {
				t.Errorf("CallProposalHash did not change when %s changed", name)
			}
		})
	}
}

func TestCallProposalHashStable(t *testing.T) {
	c := base_callProposalHashFixture()
	if CallProposalHash(c) != CallProposalHash(c) {
		t.Error("CallProposalHash is not stable/deterministic for identical input")
	}
}

func base_callProposalHashFixture() CallRecord {
	return CallRecord{
		SessionID: "s1", ConversationID: "conv1", Operation: OperationInference,
		Principal:               Principal{SessionID: "s1", TaskID: "t1", Authority: AuthorityAgent},
		ServiceActor:            Principal{SessionID: "s1", TaskID: "t1", Authority: AuthorityHarness},
		BaseConversationVersion: 1, SemanticSeq: 1, Epoch: 1,
		PolicyVersion: "p1", DescriptorVersion: "d1",
		RequestHash: "sha256:" + strings.Repeat("a", 64), ManifestHash: "sha256:" + strings.Repeat("b", 64),
	}
}

// --- CallRecord ---------------------------------------------------------------

func validCallRecord() CallRecord {
	request := []byte(`{"foo":"bar"}`)
	principal := Principal{SessionID: "s1", Authority: AuthorityAgent}
	serviceActor := Principal{SessionID: "s1", Authority: AuthorityHarness}
	c := CallRecord{
		CallID: "call_1", SessionID: "s1", ConversationID: "conv_1",
		Operation: OperationInference, State: CallPrepared,
		Principal: principal, ServiceActor: serviceActor,
		BaseConversationVersion: 1, SemanticSeq: 1,
		RequestHash: HashBytes(request), Request: request,
		ManifestHash: HashBytes([]byte("manifest")),
		PreparedSeq:  1, Revision: 1,
	}
	c.ProposalHash = CallProposalHash(c)
	return c
}

func TestCallRecordValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(CallRecord) CallRecord
		wantErr error
	}{
		{"valid PREPARED", func(c CallRecord) CallRecord { return c }, nil},
		{"missing call id", func(c CallRecord) CallRecord { c.CallID = ""; return c }, ErrInvalidRecord},
		{"missing session", func(c CallRecord) CallRecord { c.SessionID = ""; return c }, ErrInvalidRecord},
		{"missing conversation", func(c CallRecord) CallRecord { c.ConversationID = ""; return c }, ErrInvalidRecord},
		{"invalid operation", func(c CallRecord) CallRecord { c.Operation = "bogus"; return c }, ErrInvalidRecord},
		{"invalid state", func(c CallRecord) CallRecord { c.State = "bogus"; return c }, ErrInvalidRecord},
		{
			"invalid principal propagates",
			func(c CallRecord) CallRecord { c.Principal = Principal{SessionID: "s1", Authority: "bogus"}; return c },
			ErrInvalidRecord,
		},
		{
			"invalid service actor propagates",
			func(c CallRecord) CallRecord {
				c.ServiceActor = Principal{SessionID: "s1", Authority: "bogus"}
				return c
			},
			ErrInvalidRecord,
		},
		{
			"principal belongs to another session",
			func(c CallRecord) CallRecord {
				c.Principal = Principal{SessionID: "other", Authority: AuthorityAgent}
				return c
			},
			ErrInvalidRecord,
		},
		{
			"service actor belongs to another session",
			func(c CallRecord) CallRecord {
				c.ServiceActor = Principal{SessionID: "other", Authority: AuthorityHarness}
				return c
			},
			ErrInvalidRecord,
		},
		{"malformed request hash", func(c CallRecord) CallRecord { c.RequestHash = "nope"; return c }, ErrInvalidRecord},
		{
			"request hash does not match request bytes",
			func(c CallRecord) CallRecord { c.Request = []byte("tampered"); return c },
			ErrInvalidRecord,
		},
		{"zero prepared seq", func(c CallRecord) CallRecord { c.PreparedSeq = 0; return c }, ErrInvalidRecord},
		{"zero revision", func(c CallRecord) CallRecord { c.Revision = 0; return c }, ErrInvalidRecord},
		{
			"non-terminal state with a finished seq rejected",
			func(c CallRecord) CallRecord { c.FinishedSeq = 5; return c },
			ErrInvalidRecord,
		},
		{
			"terminal state without a finished seq rejected",
			func(c CallRecord) CallRecord {
				c.State = CallCompleted
				c.Attempts = 1
				response := []byte("response bytes")
				outcome := &CallOutcome{Attempt: 1, State: CallCompleted, ResponseHash: HashBytes(response), Response: response}
				c.Outcome = outcome
				c.OutcomeHash = outcome.OutcomeHash()
				// FinishedSeq deliberately left at 0.
				return c
			},
			ErrInvalidRecord,
		},
		{
			"terminal state with a finished seq ok",
			func(c CallRecord) CallRecord {
				c.State = CallCompleted
				c.FinishedSeq = 5
				c.Attempts = 1
				response := []byte("response bytes")
				outcome := &CallOutcome{Attempt: 1, State: CallCompleted, ResponseHash: HashBytes(response), Response: response}
				c.Outcome = outcome
				c.OutcomeHash = outcome.OutcomeHash()
				return c
			},
			nil,
		},
		{
			"proposal hash does not match frozen fields",
			func(c CallRecord) CallRecord { c.ProposalHash = "sha256:" + strings.Repeat("f", 64); return c },
			ErrInvalidRecord,
		},
		{
			"proposal hash stale after a frozen field changes",
			func(c CallRecord) CallRecord {
				// SemanticSeq is one of CallProposalHash's frozen inputs; a
				// record claiming an unchanged ProposalHash after it moved
				// must be rejected, not silently accepted.
				c.SemanticSeq = 99
				return c
			},
			ErrInvalidRecord,
		},
		{
			"completed outcome with matching hash ok",
			func(c CallRecord) CallRecord {
				c.State = CallCompleted
				c.FinishedSeq = 5
				c.Attempts = 1
				response := []byte("response bytes")
				outcome := &CallOutcome{Attempt: 1, State: CallCompleted, ResponseHash: HashBytes(response), Response: response}
				c.Outcome = outcome
				c.OutcomeHash = outcome.OutcomeHash()
				return c
			},
			nil,
		},
		{
			"invalid outcome propagates",
			func(c CallRecord) CallRecord {
				c.State = CallCompleted
				c.FinishedSeq = 5
				outcome := &CallOutcome{Attempt: 0, State: CallCompleted} // Attempt < 1 is invalid
				c.Outcome = outcome
				c.OutcomeHash = outcome.OutcomeHash()
				return c
			},
			ErrInvalidRecord,
		},
		{
			"outcome hash does not match outcome",
			func(c CallRecord) CallRecord {
				c.State = CallCompleted
				c.FinishedSeq = 5
				c.Attempts = 1
				response := []byte("response bytes")
				outcome := &CallOutcome{Attempt: 1, State: CallCompleted, ResponseHash: HashBytes(response), Response: response}
				c.Outcome = outcome
				c.OutcomeHash = "sha256:" + strings.Repeat("9", 64) // does not match outcome.OutcomeHash()
				return c
			},
			ErrInvalidRecord,
		},
		{
			"outcome state disagrees with call state",
			func(c CallRecord) CallRecord {
				c.State = CallCompleted
				c.FinishedSeq = 5
				c.Attempts = 1
				outcome := &CallOutcome{Attempt: 1, State: CallFailed, FailureReason: "boom"}
				c.Outcome = outcome
				c.OutcomeHash = outcome.OutcomeHash()
				return c
			},
			ErrInvalidRecord,
		},
		{
			"outcome attempt disagrees with call attempts",
			func(c CallRecord) CallRecord {
				c.State = CallCompleted
				c.FinishedSeq = 5
				c.Attempts = 2 // outcome closes attempt 1, not 2
				response := []byte("response bytes")
				outcome := &CallOutcome{Attempt: 1, State: CallCompleted, ResponseHash: HashBytes(response), Response: response}
				c.Outcome = outcome
				c.OutcomeHash = outcome.OutcomeHash()
				return c
			},
			ErrInvalidRecord,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.mutate(validCallRecord()).Validate()
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("Validate() = %v, want error wrapping %v", err, c.wantErr)
			}
		})
	}
}

// TestCallRecordValidate_TerminalEvidenceMatrix is the dedicated matrix for
// contract v3's terminal-evidence rule: each state carries exactly the
// evidence that ended it. PREPARED/SENT/UNKNOWN (non-terminal) carry no
// outcome; COMPLETED requires one; FAILED requires one, OR (no outcome) a
// cancellation reason with zero attempts; ABANDONED requires a reason and no
// outcome.
func TestCallRecordValidate_TerminalEvidenceMatrix(t *testing.T) {
	response := []byte("response bytes")
	completedOutcome := func() *CallOutcome {
		return &CallOutcome{Attempt: 1, State: CallCompleted, ResponseHash: HashBytes(response), Response: response}
	}
	failedOutcome := func() *CallOutcome {
		return &CallOutcome{Attempt: 1, State: CallFailed, FailureReason: "known failure"}
	}

	// apply builds a record from validCallRecord() with the state/evidence
	// combination under test; it always sets OutcomeHash to match a
	// non-nil Outcome so only the state-machine rule under test can fail.
	apply := func(state CallState, outcome *CallOutcome, attempts int, reason string, finishedSeq uint64) CallRecord {
		c := validCallRecord()
		c.State = state
		c.Attempts = attempts
		c.Reason = reason
		c.FinishedSeq = finishedSeq
		c.Outcome = outcome
		if outcome != nil {
			c.OutcomeHash = outcome.OutcomeHash()
		}
		return c
	}

	cases := []struct {
		name    string
		c       CallRecord
		wantErr error
	}{
		{"PREPARED with no outcome ok", apply(CallPrepared, nil, 0, "", 0), nil},
		{"SENT with no outcome ok", apply(CallSent, nil, 1, "", 0), nil},
		{"UNKNOWN with no outcome ok", apply(CallUnknown, nil, 1, "", 0), nil},
		{
			"PREPARED carrying an outcome rejected",
			apply(CallPrepared, completedOutcome(), 1, "", 0),
			ErrInvalidRecord,
		},
		{
			"SENT carrying an outcome rejected",
			apply(CallSent, completedOutcome(), 1, "", 0),
			ErrInvalidRecord,
		},
		{
			"UNKNOWN carrying an outcome rejected",
			apply(CallUnknown, completedOutcome(), 1, "", 0),
			ErrInvalidRecord,
		},
		{
			"COMPLETED with a matching outcome ok",
			apply(CallCompleted, completedOutcome(), 1, "", 5),
			nil,
		},
		{
			"COMPLETED without an outcome rejected",
			apply(CallCompleted, nil, 1, "", 5),
			ErrInvalidRecord,
		},
		{
			"FAILED with a known-failure outcome ok",
			apply(CallFailed, failedOutcome(), 1, "", 5),
			nil,
		},
		{
			"FAILED cancellation before any attempt ok (reason, no outcome, zero attempts)",
			apply(CallFailed, nil, 0, "cancelled before dispatch", 5),
			nil,
		},
		{
			"FAILED with neither an outcome nor a reason rejected",
			apply(CallFailed, nil, 0, "", 5),
			ErrInvalidRecord,
		},
		{
			"FAILED without an outcome but with attempts made rejected",
			apply(CallFailed, nil, 1, "should have an outcome", 5),
			ErrInvalidRecord,
		},
		{
			"ABANDONED with a reason and no outcome ok",
			apply(CallAbandoned, nil, 2, "explicit abandonment", 5),
			nil,
		},
		{
			"ABANDONED without a reason rejected",
			apply(CallAbandoned, nil, 2, "", 5),
			ErrInvalidRecord,
		},
		{
			"ABANDONED carrying an outcome rejected",
			apply(CallAbandoned, completedOutcome(), 1, "explicit abandonment", 5),
			ErrInvalidRecord,
		},
		{
			"an outcome hash without an outcome is rejected regardless of state",
			func() CallRecord {
				c := apply(CallPrepared, nil, 0, "", 0)
				c.OutcomeHash = "sha256:" + strings.Repeat("a", 64) // no Outcome to back it
				return c
			}(),
			ErrInvalidRecord,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.c.Validate()
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("Validate() = %v, want error wrapping %v", err, c.wantErr)
			}
		})
	}
}

func TestCallRecordClone(t *testing.T) {
	in := int64(7)
	c := validCallRecord()
	c.Outcome = &CallOutcome{
		State: CallCompleted, Response: []byte("resp"),
		Usage: []UsageIteration{{Iteration: 1, InputTokens: &in}},
	}

	clone := c.Clone()
	clone.Request[0] = 'X'
	clone.Outcome.Response[0] = 'X'
	*clone.Outcome.Usage[0].InputTokens = 999

	if c.Request[0] == 'X' {
		t.Error("mutating clone.Request affected the original")
	}
	if c.Outcome.Response[0] == 'X' {
		t.Error("mutating clone.Outcome.Response affected the original")
	}
	if *c.Outcome.Usage[0].InputTokens != 7 {
		t.Error("mutating clone.Outcome.Usage affected the original")
	}
}

func TestCallRecordClone_NilUsageStaysNil(t *testing.T) {
	c := validCallRecord()
	c.Outcome = &CallOutcome{State: CallCompleted} // Usage left nil
	clone := c.Clone()
	if clone.Outcome.Usage != nil {
		t.Error("Clone() populated Outcome.Usage that was nil on the original")
	}
}

func TestCallRecordClone_NilOutcomeStaysNil(t *testing.T) {
	c := validCallRecord() // Outcome is nil
	clone := c.Clone()
	if clone.Outcome != nil {
		t.Error("Clone() populated Outcome that was nil on the original")
	}
}

// --- Conversation ---------------------------------------------------------

func TestConversationIDForDeterministic(t *testing.T) {
	id1 := ConversationIDFor("t1", "a1")
	id2 := ConversationIDFor("t1", "a1")
	if id1 != id2 {
		t.Error("ConversationIDFor is not deterministic for identical inputs")
	}
	if ConversationIDFor("t1", "a2") == id1 {
		t.Error("ConversationIDFor collided across different agent IDs")
	}
	if ConversationIDFor("t2", "a1") == id1 {
		t.Error("ConversationIDFor collided across different task IDs")
	}
}

func TestConversationValidate(t *testing.T) {
	base := Conversation{SessionID: "s1", ConversationID: "c1", TaskID: "t1", AgentID: "a1", Version: 1, Revision: 1}
	if err := base.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	cases := []struct {
		name   string
		mutate func(Conversation) Conversation
	}{
		{"missing session", func(c Conversation) Conversation { c.SessionID = ""; return c }},
		{"missing conversation id", func(c Conversation) Conversation { c.ConversationID = ""; return c }},
		{"missing task", func(c Conversation) Conversation { c.TaskID = ""; return c }},
		{"missing agent", func(c Conversation) Conversation { c.AgentID = ""; return c }},
		{"zero version", func(c Conversation) Conversation { c.Version = 0; return c }},
		{"zero revision", func(c Conversation) Conversation { c.Revision = 0; return c }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.mutate(base).Validate(); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("Validate() = %v, want ErrInvalidRecord", err)
			}
		})
	}
}

// --- CallAttempt ------------------------------------------------------------

// TestCallAttemptValidate covers the structural rules plus contract v3's
// open/closed state consistency: SENT/UNKNOWN are open (no finish or
// outcome), COMPLETED/FAILED are closed (both required), ABANDONED is
// finished without an outcome, and Retryable is meaningful only on FAILED.
func TestCallAttemptValidate(t *testing.T) {
	validHash := "sha256:" + strings.Repeat("a", 64)
	openAttempt := CallAttempt{CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptSent, SentSeq: 1}
	if err := openAttempt.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	cases := []struct {
		name    string
		a       CallAttempt
		wantErr error
	}{
		{"valid open SENT attempt", openAttempt, nil},
		{
			"valid open UNKNOWN attempt",
			CallAttempt{CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptUnknown, SentSeq: 1},
			nil,
		},
		{
			"valid closed COMPLETED attempt",
			CallAttempt{
				CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptCompleted,
				SentSeq: 1, FinishedSeq: 2, OutcomeHash: validHash,
			},
			nil,
		},
		{
			"valid closed FAILED attempt, not retryable",
			CallAttempt{
				CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptFailed,
				SentSeq: 1, FinishedSeq: 2, OutcomeHash: validHash,
			},
			nil,
		},
		{
			"valid closed FAILED attempt, retryable",
			CallAttempt{
				CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptFailed,
				SentSeq: 1, FinishedSeq: 2, OutcomeHash: validHash, Retryable: true,
			},
			nil,
		},
		{
			"valid finished ABANDONED attempt with no outcome",
			CallAttempt{CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptAbandoned, SentSeq: 1, FinishedSeq: 2},
			nil,
		},
		{
			"missing call id",
			CallAttempt{SessionID: "s1", Attempt: 1, State: AttemptSent, SentSeq: 1},
			ErrInvalidRecord,
		},
		{
			"missing session",
			CallAttempt{CallID: "call_1", Attempt: 1, State: AttemptSent, SentSeq: 1},
			ErrInvalidRecord,
		},
		{
			"attempt below 1",
			CallAttempt{CallID: "call_1", SessionID: "s1", Attempt: 0, State: AttemptSent, SentSeq: 1},
			ErrInvalidRecord,
		},
		{
			"zero sent seq",
			CallAttempt{CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptSent, SentSeq: 0},
			ErrInvalidRecord,
		},
		{
			"invalid state",
			CallAttempt{CallID: "call_1", SessionID: "s1", Attempt: 1, State: "bogus", SentSeq: 1},
			ErrInvalidRecord,
		},
		{
			"malformed outcome hash",
			CallAttempt{
				CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptCompleted,
				SentSeq: 1, FinishedSeq: 2, OutcomeHash: "not-a-hash",
			},
			ErrInvalidRecord,
		},
		{
			"retryable on a non-FAILED (SENT) state rejected",
			CallAttempt{CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptSent, SentSeq: 1, Retryable: true},
			ErrInvalidRecord,
		},
		{
			"retryable on COMPLETED rejected",
			CallAttempt{
				CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptCompleted,
				SentSeq: 1, FinishedSeq: 2, OutcomeHash: validHash, Retryable: true,
			},
			ErrInvalidRecord,
		},
		{
			"open SENT with a finish sequence rejected",
			CallAttempt{CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptSent, SentSeq: 1, FinishedSeq: 2},
			ErrInvalidRecord,
		},
		{
			"open UNKNOWN with an outcome hash rejected",
			CallAttempt{CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptUnknown, SentSeq: 1, OutcomeHash: validHash},
			ErrInvalidRecord,
		},
		{
			"closed COMPLETED missing finish sequence rejected",
			CallAttempt{CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptCompleted, SentSeq: 1, OutcomeHash: validHash},
			ErrInvalidRecord,
		},
		{
			"closed FAILED missing outcome hash rejected",
			CallAttempt{CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptFailed, SentSeq: 1, FinishedSeq: 2},
			ErrInvalidRecord,
		},
		{
			"ABANDONED with an outcome hash rejected",
			CallAttempt{
				CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptAbandoned,
				SentSeq: 1, FinishedSeq: 2, OutcomeHash: validHash,
			},
			ErrInvalidRecord,
		},
		{
			"ABANDONED without a finish sequence rejected",
			CallAttempt{CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptAbandoned, SentSeq: 1},
			ErrInvalidRecord,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.a.Validate()
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("Validate() = %v, want error wrapping %v", err, c.wantErr)
			}
		})
	}
}

// TestValidAttemptTransitionMatrix exhaustively checks every (from, to) pair
// among the five attempt states: SENT closes as COMPLETED, FAILED, or
// UNKNOWN; UNKNOWN reconciles to COMPLETED, FAILED, or ABANDONED; every
// other state (including the closed/terminal ones as a "from") has no valid
// outgoing transition.
func TestValidAttemptTransitionMatrix(t *testing.T) {
	allowed := map[[2]AttemptState]bool{
		{AttemptSent, AttemptCompleted}:    true,
		{AttemptSent, AttemptFailed}:       true,
		{AttemptSent, AttemptUnknown}:      true,
		{AttemptUnknown, AttemptCompleted}: true,
		{AttemptUnknown, AttemptFailed}:    true,
		{AttemptUnknown, AttemptAbandoned}: true,
	}
	states := []AttemptState{AttemptSent, AttemptCompleted, AttemptFailed, AttemptUnknown, AttemptAbandoned}
	for _, from := range states {
		for _, to := range states {
			want := allowed[[2]AttemptState{from, to}]
			if got := ValidAttemptTransition(from, to); got != want {
				t.Errorf("ValidAttemptTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestValidAttemptTransition_ClosedAttemptsAreImmutable(t *testing.T) {
	states := []AttemptState{AttemptSent, AttemptCompleted, AttemptFailed, AttemptUnknown, AttemptAbandoned}
	for _, from := range []AttemptState{AttemptCompleted, AttemptFailed, AttemptAbandoned} {
		for _, to := range states {
			if ValidAttemptTransition(from, to) {
				t.Errorf("ValidAttemptTransition(%s, %s) = true, want false (closed attempt)", from, to)
			}
		}
	}
}

func TestValidAttemptTransition_NoSelfOrBackwardLoop(t *testing.T) {
	if ValidAttemptTransition(AttemptSent, AttemptSent) {
		t.Error("SENT -> SENT must be false")
	}
	if ValidAttemptTransition(AttemptUnknown, AttemptSent) {
		t.Error("UNKNOWN -> SENT must be false: no automatic resend")
	}
}
