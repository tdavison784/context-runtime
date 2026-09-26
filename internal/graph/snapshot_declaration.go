package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// planSnapshotDeclaration freezes one ordered authority/boundary partition.
// Occurrence IDs identify the audit, while member declaration signatures identify
// its meaning. No lifecycle state is copied into the signature at comparison time.
func planSnapshotDeclaration(tx store.Tx, reader store.SemanticReader, members []domain.ContextItem, eventID string) (domain.SnapshotDeclaration, error) {
	if len(members) == 0 || len(members) > maxSnapshotMembers {
		return domain.SnapshotDeclaration{}, store.ErrLimitExceeded
	}
	first := members[0]
	d := domain.SnapshotDeclaration{SemanticMeta: domain.SemanticMeta{ID: deriveID("snapshot", "context-runtime/graph/snapshot-declaration-id/v1", tx.SessionID(), eventID, first.ID), SessionID: tx.SessionID(), SchemaVersion: domain.SemanticSchemaV1}, TaskID: first.TaskID, Authority: first.Authority, Access: first.Access, LegacyKnown: true}
	for _, item := range members {
		if item.SessionID != d.SessionID || item.TaskID != d.TaskID || item.Authority != d.Authority || item.Access != d.Access || item.Section != domain.SectionWorking || item.Namespace != domain.NamespaceDirective || !tx.Allocated(item.Seq) {
			return d, ErrSnapshotMemberConflict
		}
		declaration, err := checkedDeclaration(reader, item)
		if err != nil {
			return d, err
		}
		if !declaration.LegacyKnown || !tx.Allocated(declaration.Seq) {
			return d, domain.ErrInvalidRecord
		}
		if d.PolicyVersion == "" {
			d.PolicyVersion = declaration.PolicyVersion
		}
		if declaration.PolicyVersion != d.PolicyVersion {
			return d, domain.ErrInvalidRecord
		}
		d.Members = append(d.Members, domain.SnapshotDeclarationMember{ItemID: item.ID, DeclarationID: declaration.ID, Signature: declaration.Signature})
	}
	d.Seq = tx.NextSeq()
	var err error
	d.Signature, err = d.CanonicalSignature()
	if err != nil {
		return d, err
	}
	return d, d.Validate()
}
