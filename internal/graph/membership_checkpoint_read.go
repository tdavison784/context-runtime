package graph

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// CheckpointOfItem returns the checkpoint whose dedicated CHECKPOINT item is
// itemID and whether it is the newest checkpoint of its conversation, so GC can
// protect only the newest one (W3 request). A missing, private, or
// non-checkpoint item is ErrNotFound. workLimit counts checkpoint records read;
// reaching it fails rather than guessing.
func CheckpointOfItem(tx store.ReadTx, viewer domain.Principal, itemID string, pageSize, workLimit int) (domain.Checkpoint, bool, error) {
	var none domain.Checkpoint
	it, err := readViewableItem(tx, viewer, itemID)
	if err != nil {
		return none, false, err
	}
	if it.Role != domain.RoleCheckpoint || it.TaskID == "" || it.AgentID == "" {
		return none, false, domain.ErrNotFound
	}
	sem, err := store.ReadSemantic(tx)
	if err != nil {
		return none, false, err
	}
	conversation := domain.ConversationIDFor(it.TaskID, it.AgentID)
	found, index := none, -1
	err = scanCheckpoints(sem, viewer, conversation, pageSize, workLimit, func(n int, c domain.Checkpoint) bool {
		if c.ItemID == itemID {
			found, index = c, n
			return false
		}
		return true
	})
	if err != nil {
		return none, false, err
	}
	if index < 0 {
		return none, false, domain.ErrIntegrity // a CHECKPOINT item always has its companion
	}
	return found, index == 0, nil
}

// CheckpointsCoveringItem returns, newest first, every checkpoint visible to
// viewer whose closed covered prefix contains an exchange that itemID is a
// member of. Generation-input provenance is not coverage: a requirement read by
// a checkpoint is never covered by it. The result is complete or an error.
//
// Access before limit (XREV-1.3): a checkpoint is visible only to a viewer
// with its conversation's exact owners, so only the viewer's own conversation
// is consulted. Cost is independent of history (H2, SPEC-2.7, XREV-2.3): one
// exact read of the write-time (conversation, item) index gives the earliest
// round containing the item, then only the checkpoints that cover it, plus
// the one that stops the scan, are read.
func CheckpointsCoveringItem(tx store.ReadTx, viewer domain.Principal, itemID string, pageSize, workLimit int) ([]domain.Checkpoint, error) {
	if _, err := readViewableItem(tx, viewer, itemID); err != nil {
		return nil, err
	}
	if viewer.TaskID == "" || viewer.AgentID == "" {
		return nil, nil // no conversation-bounded checkpoint is visible
	}
	sem, err := store.ReadSemantic(tx)
	if err != nil {
		return nil, err
	}
	conversation := domain.ConversationIDFor(viewer.TaskID, viewer.AgentID)
	earliest, err := sem.EarliestExchangeWithItem(conversation, itemID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if earliest.Validate() != nil || earliest.ConversationID != conversation || earliest.SessionID != viewer.SessionID {
		return nil, domain.ErrIntegrity
	}
	var out []domain.Checkpoint
	err = scanCheckpoints(sem, viewer, conversation, pageSize, workLimit-1, func(_ int, c domain.Checkpoint) bool {
		if c.CoveredFrontier < earliest.Ordinal {
			return false // chains never regress: older ones cover less
		}
		out = append(out, c)
		return true
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// scanCheckpoints visits a conversation's visible checkpoints newest first
// until visit returns false, failing with ErrResourceLimit rather than
// stopping early at workLimit.
func scanCheckpoints(sem store.MembershipReader, viewer domain.Principal, conversation string, pageSize, workLimit int, visit func(int, domain.Checkpoint) bool) error {
	if pageSize <= 0 || workLimit <= 0 {
		return domain.ErrResourceLimit
	}
	var after store.Cursor
	n := 0
	for {
		page, err := sem.CheckpointsByConversation(viewer, conversation, store.Page{After: after, Limit: min(pageSize, workLimit-n)})
		if err != nil {
			return err
		}
		for _, c := range page.Records {
			if c.Validate() != nil || c.SessionID != viewer.SessionID || c.ConversationID != conversation {
				return domain.ErrIntegrity
			}
			if !visit(n, c) {
				return nil
			}
			n++
		}
		if !page.More {
			return nil
		}
		if n >= workLimit || len(page.Records) == 0 || page.Next == after {
			return domain.ErrResourceLimit
		}
		after = page.Next
	}
}

func readViewableItem(tx store.ReadTx, viewer domain.Principal, itemID string) (domain.ContextItem, error) {
	if viewer.Validate() != nil || viewer.SessionID != tx.SessionID() {
		return domain.ContextItem{}, domain.ErrNotFound
	}
	it, err := tx.Item(itemID)
	if errors.Is(err, domain.ErrNotFound) || err == nil && !it.Access.Permits(viewer) {
		return domain.ContextItem{}, domain.ErrNotFound
	}
	return it, err
}
