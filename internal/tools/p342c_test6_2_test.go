package tools

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TEST-6.2 (r6-test6 line 52): the manifest.Principal conjunct of
// CreateCheckpoint's identity check (checkpoint.go:53) was never the deciding
// layer in any test — every foreign-manifest probe also named another
// principal's call, so call.Principal at checkpoint.go:60 refused first and
// dropping the conjunct survived the whole tools suite (r6-test6 line 38).
//
// This probe isolates the conjunct, on memory and sqlitetest. The working
// GENERATION_INPUT manifest of a checkpointable round is re-seeded as a
// direct store write with ONE field changed: its principal names another
// workflow's copy of the same agent. The conversation ID is derived from
// TaskID+AgentID only, so the forged record still validates and still names
// this conversation, this exchange, this coverage — and its CallID is the
// round's own completed call BY the checkpointing agent, so the call-level
// identity check at :60 passes. Only the manifest-level conjunct can refuse:
// the probe expects the not-found tool error and no state written, while the
// untampered manifest through the same round still writes a checkpoint.
// Removing the manifest.Principal comparison lets the forged manifest
// through and fails this test.
func TestTEST6_2_ForgedManifestPrincipalRejectedThoughTheCallIsYours(t *testing.T) {
	p342bEachStore(t, func(t *testing.T, st store.Store) {
		i := seedAgentInvocation(t, st, "agent")
		_, s, i2, manifest := checkpointConversationOn(t, st, i, true)

		var forged domain.AdmissionManifest
		update(t, st, func(tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			m, err := sem.AdmissionManifest(manifest)
			if err != nil {
				return err
			}
			// Setup facts: the real manifest carries the agent's exact
			// principal and this conversation, which is why it passes :53.
			if m.Principal != i2.Principal || m.ConversationID != i2.ConversationID || m.Purpose != domain.AdmissionGenerationInput {
				t.Fatalf("working manifest = %+v", m)
			}
			forged = m
			forged.ID, forged.Seq = "adm-forged-t62", tx.NextSeq()
			forged.Principal = m.Principal
			forged.Principal.WorkflowID = "wf-forged"
			if err := forged.Validate(); err != nil {
				t.Fatalf("forged manifest does not validate: %v", err)
			}
			if forged.Principal == i2.Principal || forged.ConversationID != i2.ConversationID || forged.CallID != i2.CallID {
				t.Fatalf("forged manifest = %+v", forged)
			}
			return sem.InsertAdmissionManifest(forged)
		})

		// The probe: through a second tool call of the same round — same
		// exchange, same producing call — the forged manifest must be
		// refused by the manifest-level principal check alone.
		probe := addToolCall(t, st, i2, "tool-t62-probe")
		rejectedCheckpoint(t, st, s, probe, summary("t62", forged.ID, "summary"), domain.ToolErrorNotFound)

		// Control: the same round's untampered manifest still writes, so the
		// refusal names the manifest's principal, not the round's shape.
		ctrl := addToolCall(t, st, i2, "tool-t62-ctrl")
		var r domain.ToolResult
		update(t, st, func(tx store.Tx) error {
			var err error
			r, err = s.CreateCheckpoint(tx, dispatcher(ctrl), Request[domain.CheckpointIntent]{ctrl, summary("t62-ok", manifest, "summary")}, tx.NextSeq())
			return err
		})
		if r.CheckpointID == "" {
			t.Fatal("control checkpoint rejected")
		}
	})
}
