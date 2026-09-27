package graph

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// rawTranscript is an unparsed transcript row as ingestion writes it for a
// span of the given authority (policy.ForTranscript kinds), kept task-scoped
// so it lies within a task-scoped derived item's boundary.
func rawTranscript(sess, id string, seq uint64, authority domain.Authority, kind domain.Kind) domain.ContextItem {
	it := storetest.NewItem(sess, id, seq, "raw "+id)
	it.Role, it.Authority, it.Kind = domain.RoleTranscript, authority, kind
	it.Generation, it.Retention = domain.GenerationEphemeral, domain.RetentionLow
	it.Scope = domain.ScopeTask
	it.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: it.TaskID}
	return it
}

// transcriptCases is the FR-DOM-006 ruling: only a TOOL-authority tool_result
// transcript is evidence; every conversation transcript, and any source
// the ruling does not name, stays provenance-only (fail closed).
var transcriptCases = []struct {
	authority domain.Authority
	kind      domain.Kind
	evidence  bool
}{
	{domain.AuthorityTool, domain.KindToolResult, true},
	{domain.AuthorityTool, domain.KindEvidence, false},
	{domain.AuthorityUser, domain.KindUserMessage, false},
	{domain.AuthorityAgent, domain.KindAssistantMessage, false},
	{domain.AuthoritySystem, domain.KindConversation, false},
	{domain.AuthorityHarness, domain.KindConversation, false},
	{domain.AuthorityRetrievedContent, domain.KindEvidence, false},
}

// TestEvidenceSupportQualifiesOnlyToolResultTranscripts: a keyed write or
// completion claim citing a raw TOOL result as EVIDENCE_SUPPORT succeeds;
// citing a USER/AGENT/SYSTEM/HARNESS transcript is rejected as support but
// remains valid PROVENANCE.
func TestEvidenceSupportQualifiesOnlyToolResultTranscripts(t *testing.T) {
	for _, tc := range transcriptCases {
		t.Run(fmt.Sprintf("%s/%s", tc.authority, tc.kind), func(t *testing.T) {
			eachStore(t, func(t *testing.T, s store.Store) {
				const sess = "sess-evidence"
				actor := principal(sess, domain.AuthorityUser)
				for _, purpose := range []domain.CoveragePurpose{domain.CoverageEvidenceSupport, domain.CoverageProvenance} {
					err := s.Update(ctx, sess, func(tx store.Tx) error {
						src := rawTranscript(sess, "raw-"+string(purpose), tx.NextSeq(), tc.authority, tc.kind)
						derived := taskItem(sess, "fact-"+string(purpose), tx.NextSeq(), domain.AuthorityUser)
						mustInsert(t, tx, src, derived)
						_, err := LinkDerivedCoverage(tx, actor, derived.ID, []string{src.ID}, purpose, "evt", 4)
						return err
					})
					want := purpose == domain.CoverageProvenance || tc.evidence
					if want && err != nil {
						t.Errorf("%s: err = %v, want accepted", purpose, err)
					}
					if !want && !errors.Is(err, domain.ErrInvalidRecord) {
						t.Errorf("%s: err = %v, want ErrInvalidRecord (provenance-only transcript)", purpose, err)
					}
				}
			})
		})
	}
}

// TestCreationDeclarationSupportQualifiesOnlyToolResultTranscripts is the
// same rule for a keyed write's declared cited support (context_remember).
func TestCreationDeclarationSupportQualifiesOnlyToolResultTranscripts(t *testing.T) {
	for _, tc := range transcriptCases {
		t.Run(fmt.Sprintf("%s/%s", tc.authority, tc.kind), func(t *testing.T) {
			eachStore(t, func(t *testing.T, s store.Store) {
				const sess = "sess-evidence-decl"
				err := s.Update(ctx, sess, func(tx store.Tx) error {
					src := rawTranscript(sess, "raw", tx.NextSeq(), tc.authority, tc.kind)
					keyed := agentDirective(sess, "keyed", domain.AgentKeyID("status"), tx.NextSeq())
					mustInsert(t, tx, src, keyed)
					_, err := DeclareCreation(tx, keyed, CreationAcceptance{PolicyVersion: testDeclarationPolicy, SupportIDs: []string{src.ID}})
					return err
				})
				if tc.evidence && err != nil {
					t.Errorf("err = %v, want TOOL result accepted as cited support", err)
				}
				if !tc.evidence && !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
					t.Errorf("err = %v, want ErrInvalidAuthorityPromotion (provenance-only transcript)", err)
				}
			})
		})
	}
}
