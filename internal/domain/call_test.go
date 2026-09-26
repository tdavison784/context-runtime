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
			func(c CallRecord) CallRecord { c.State = CallCompleted; return c },
			ErrInvalidRecord,
		},
		{
			"terminal state with a finished seq ok",
			func(c CallRecord) CallRecord { c.State = CallCompleted; c.FinishedSeq = 5; return c },
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
				response := []byte("response bytes")
				outcome := &CallOutcome{Attempt: 1, State: CallCompleted, ResponseHash: HashBytes(response), Response: response}
				c.Outcome = outcome
				c.OutcomeHash = "sha256:" + strings.Repeat("9", 64) // does not match outcome.OutcomeHash()
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

func TestCallAttemptValidate(t *testing.T) {
	base := CallAttempt{CallID: "call_1", SessionID: "s1", Attempt: 1, State: AttemptSent, SentSeq: 1}
	if err := base.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	cases := []struct {
		name   string
		mutate func(CallAttempt) CallAttempt
	}{
		{"missing call id", func(a CallAttempt) CallAttempt { a.CallID = ""; return a }},
		{"missing session", func(a CallAttempt) CallAttempt { a.SessionID = ""; return a }},
		{"attempt below 1", func(a CallAttempt) CallAttempt { a.Attempt = 0; return a }},
		{"zero sent seq", func(a CallAttempt) CallAttempt { a.SentSeq = 0; return a }},
		{"invalid state", func(a CallAttempt) CallAttempt { a.State = "bogus"; return a }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.mutate(base).Validate(); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("Validate() = %v, want ErrInvalidRecord", err)
			}
		})
	}
}
