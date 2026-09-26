package memory

import (
	"iter"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// The Phase 3 semantic facet (store.SemanticTxBase) lives beside the legacy
// transaction on the same overlay: its tables fold into the session state
// with the transaction and are discarded with it, and its writes share the
// Guard's poison state through store.Semantic (P3-1).

type ownerKey struct {
	kind domain.OwnerKind
	id   string
}

type covSourceKey struct {
	itemID  string
	purpose domain.CoveragePurpose
}

type convOrdinal struct {
	conv    string
	ordinal uint64
}

type exchangePos struct {
	exchange string
	position uint64
}

type receiptKey struct {
	family  domain.MutationFamily
	request string
}

// semState is one session's committed Phase 3 companion records and their
// keyed indexes.
type semState struct {
	owners       map[ownerKey]domain.OwnerRegistration
	coverages    map[string]domain.CoverageRecord
	covMembers   map[string][]domain.CoverageMember // sorted by key (== ID)
	covBySource  map[covSourceKey][]seqRef
	exchanges    map[string]domain.LogicalExchange
	exchByConv   map[string][]seqRef
	exchOrdinal  map[convOrdinal]string
	exchLast     map[string]uint64 // conversation -> highest ordinal
	openByTask   map[string][]seqRef
	members      map[string]domain.ExchangeMember
	memberPos    map[exchangePos]string
	membersByEx  map[string][]seqRef
	membersByIt  map[string][]seqRef
	acks         map[string]domain.ExchangeAcknowledgment
	ackByEx      map[string]string
	admissions   map[string]domain.AdmissionManifest
	admByEx      map[string][]seqRef
	membership   map[string]domain.ConversationMembershipState
	checkpoints  map[string]domain.Checkpoint
	ckByItem     map[string]string
	ckByConv     map[string][]seqRef
	mutReceipts  map[string]domain.MutationReceipt // by receipt ID
	mutByKey     map[receiptKey]string
	toolReceipts map[string]domain.ToolExecutionReceipt
	reserving    map[string][]seqRef                   // task -> reserving calls by (PreparedSeq, CallID)
	decls        map[string]domain.CreationDeclaration // by item ID
	declIDs      map[string]string                     // declaration ID -> item ID
	snapshots    map[string]domain.SnapshotDeclaration
	grantIdx     map[grantKey][]seqRef // (action, target) -> grants by (IssuedSeq, ID)
	lcByTarget   map[lifecycleKey][]seqRef
	changes      map[string]domain.SemanticChange
	chByTarget   map[string][]seqRef // target authorization key -> changes
	res          resState
	proof        proofState
	ret          retState
	gc           gcState
}

func newSemState() *semState {
	return &semState{
		owners:       map[ownerKey]domain.OwnerRegistration{},
		coverages:    map[string]domain.CoverageRecord{},
		covMembers:   map[string][]domain.CoverageMember{},
		covBySource:  map[covSourceKey][]seqRef{},
		exchanges:    map[string]domain.LogicalExchange{},
		exchByConv:   map[string][]seqRef{},
		exchOrdinal:  map[convOrdinal]string{},
		exchLast:     map[string]uint64{},
		openByTask:   map[string][]seqRef{},
		members:      map[string]domain.ExchangeMember{},
		memberPos:    map[exchangePos]string{},
		membersByEx:  map[string][]seqRef{},
		membersByIt:  map[string][]seqRef{},
		acks:         map[string]domain.ExchangeAcknowledgment{},
		ackByEx:      map[string]string{},
		admissions:   map[string]domain.AdmissionManifest{},
		admByEx:      map[string][]seqRef{},
		membership:   map[string]domain.ConversationMembershipState{},
		checkpoints:  map[string]domain.Checkpoint{},
		ckByItem:     map[string]string{},
		ckByConv:     map[string][]seqRef{},
		mutReceipts:  map[string]domain.MutationReceipt{},
		mutByKey:     map[receiptKey]string{},
		toolReceipts: map[string]domain.ToolExecutionReceipt{},
		reserving:    map[string][]seqRef{},
		decls:        map[string]domain.CreationDeclaration{},
		declIDs:      map[string]string{},
		snapshots:    map[string]domain.SnapshotDeclaration{},
		grantIdx:     map[grantKey][]seqRef{},
		lcByTarget:   map[lifecycleKey][]seqRef{},
		changes:      map[string]domain.SemanticChange{},
		chByTarget:   map[string][]seqRef{},
		res:          newResState(),
		proof:        newProofState(),
		ret:          newRetState(),
		gc:           newGCState(),
	}
}

func cloneMembers(ms []domain.CoverageMember) []domain.CoverageMember {
	out := make([]domain.CoverageMember, len(ms))
	for i, m := range ms {
		out[i] = m.Clone()
	}
	return out
}

// semView is semState seen through one transaction.
type semView struct {
	owners       table[ownerKey, domain.OwnerRegistration]
	coverages    table[string, domain.CoverageRecord]
	covMembers   table[string, []domain.CoverageMember]
	covBySource  orderedIndex[covSourceKey]
	exchanges    table[string, domain.LogicalExchange]
	exchByConv   orderedIndex[string]
	exchOrdinal  table[convOrdinal, string]
	exchLast     table[string, uint64]
	openByTask   orderedIndex[string]
	members      table[string, domain.ExchangeMember]
	memberPos    table[exchangePos, string]
	membersByEx  orderedIndex[string]
	membersByIt  orderedIndex[string]
	acks         table[string, domain.ExchangeAcknowledgment]
	ackByEx      table[string, string]
	admissions   table[string, domain.AdmissionManifest]
	admByEx      orderedIndex[string]
	membership   table[string, domain.ConversationMembershipState]
	checkpoints  table[string, domain.Checkpoint]
	ckByItem     table[string, string]
	ckByConv     orderedIndex[string]
	mutReceipts  table[string, domain.MutationReceipt]
	mutByKey     table[receiptKey, string]
	toolReceipts table[string, domain.ToolExecutionReceipt]
	reserving    orderedIndex[string]
	decls        table[string, domain.CreationDeclaration]
	declIDs      table[string, string]
	snapshots    table[string, domain.SnapshotDeclaration]
	grantIdx     orderedIndex[grantKey]
	lcByTarget   orderedIndex[lifecycleKey]
	changes      table[string, domain.SemanticChange]
	chByTarget   orderedIndex[string]
	res          resView
	proof        proofView
	ret          retView
	gc           gcView
}

func newSemView(st *semState, w bool) semView {
	return semView{
		owners:       newTable(st.owners, w, domain.OwnerRegistration.Clone),
		coverages:    newTable(st.coverages, w, domain.CoverageRecord.Clone),
		covMembers:   newTable(st.covMembers, w, cloneMembers),
		covBySource:  newOrderedIndex(st.covBySource, w),
		exchanges:    newTable(st.exchanges, w, domain.LogicalExchange.Clone),
		exchByConv:   newOrderedIndex(st.exchByConv, w),
		exchOrdinal:  newTable(st.exchOrdinal, w, same[string]),
		exchLast:     newTable(st.exchLast, w, same[uint64]),
		openByTask:   newOrderedIndex(st.openByTask, w),
		members:      newTable(st.members, w, domain.ExchangeMember.Clone),
		memberPos:    newTable(st.memberPos, w, same[string]),
		membersByEx:  newOrderedIndex(st.membersByEx, w),
		membersByIt:  newOrderedIndex(st.membersByIt, w),
		acks:         newTable(st.acks, w, domain.ExchangeAcknowledgment.Clone),
		ackByEx:      newTable(st.ackByEx, w, same[string]),
		admissions:   newTable(st.admissions, w, domain.AdmissionManifest.Clone),
		admByEx:      newOrderedIndex(st.admByEx, w),
		membership:   newTable(st.membership, w, domain.ConversationMembershipState.Clone),
		checkpoints:  newTable(st.checkpoints, w, domain.Checkpoint.Clone),
		ckByItem:     newTable(st.ckByItem, w, same[string]),
		ckByConv:     newOrderedIndex(st.ckByConv, w),
		mutReceipts:  newTable(st.mutReceipts, w, domain.MutationReceipt.Clone),
		mutByKey:     newTable(st.mutByKey, w, same[string]),
		toolReceipts: newTable(st.toolReceipts, w, domain.ToolExecutionReceipt.Clone),
		reserving:    newOrderedIndex(st.reserving, w),
		decls:        newTable(st.decls, w, domain.CreationDeclaration.Clone),
		declIDs:      newTable(st.declIDs, w, same[string]),
		snapshots:    newTable(st.snapshots, w, domain.SnapshotDeclaration.Clone),
		grantIdx:     newOrderedIndex(st.grantIdx, w),
		lcByTarget:   newOrderedIndex(st.lcByTarget, w),
		changes:      newTable(st.changes, w, domain.SemanticChange.Clone),
		chByTarget:   newOrderedIndex(st.chByTarget, w),
		res:          newResView(&st.res, w),
		proof:        newProofView(&st.proof, w),
		ret:          newRetView(&st.ret, w),
		gc:           newGCView(&st.gc, w),
	}
}

// dirty reports whether the transaction wrote a companion record. Index
// tables change only alongside a record table.
func (v *semView) dirty() bool {
	return v.owners.dirty() || v.coverages.dirty() || v.exchanges.dirty() || v.members.dirty() ||
		v.acks.dirty() || v.admissions.dirty() || v.membership.dirty() || v.checkpoints.dirty() ||
		v.mutReceipts.dirty() || v.toolReceipts.dirty() || v.decls.dirty() || v.snapshots.dirty() ||
		v.changes.dirty() || v.res.dirty() || v.proof.dirty() || v.ret.dirty() || v.gc.dirty()
}

func (v *semView) commit() {
	v.owners.commit()
	v.coverages.commit()
	v.covMembers.commit()
	v.covBySource.commit()
	v.exchanges.commit()
	v.exchByConv.commit()
	v.exchOrdinal.commit()
	v.exchLast.commit()
	v.openByTask.commit()
	v.members.commit()
	v.memberPos.commit()
	v.membersByEx.commit()
	v.membersByIt.commit()
	v.acks.commit()
	v.ackByEx.commit()
	v.admissions.commit()
	v.admByEx.commit()
	v.membership.commit()
	v.checkpoints.commit()
	v.ckByItem.commit()
	v.ckByConv.commit()
	v.mutReceipts.commit()
	v.mutByKey.commit()
	v.toolReceipts.commit()
	v.reserving.commit()
	v.decls.commit()
	v.declIDs.commit()
	v.snapshots.commit()
	v.grantIdx.commit()
	v.lcByTarget.commit()
	v.changes.commit()
	v.chByTarget.commit()
	v.res.commit()
	v.proof.commit()
	v.ret.commit()
	v.gc.commit()
}

// semRead implements store.SemanticReader over a transaction's view.
type semRead struct{ r *readTx }

// semTx implements store.SemanticTxBase. Every write validates completely
// before touching the overlay, so a rejected write leaves the transaction
// unchanged; the Guard poisons the transaction if an earlier write
// succeeded.
type semTx struct {
	semRead
	t *tx
}

var (
	_ store.SemanticTxBase          = (*semTx)(nil)
	_ store.SemanticReader          = semRead{}
	_ store.SemanticBackendProvider = (*tx)(nil)
	_ store.SemanticReadProvider    = (*readTx)(nil)
)

// SemanticBackend implements store.SemanticBackendProvider.
func (t *tx) SemanticBackend() store.SemanticTxBase { return &semTx{semRead: semRead{t.readTx}, t: t} }

// SemanticReadBackend implements store.SemanticReadProvider.
func (r *readTx) SemanticReadBackend() store.SemanticReader { return semRead{r} }

// companion applies the checks every new companion insert shares: session,
// structural validity, and the sequence rule. It returns nothing on success;
// callers then check references and keys before writing.
func (t *tx) companion(what string, m domain.SemanticMeta, validate func() error) error {
	if err := t.own(m.SessionID); err != nil {
		return err
	}
	if err := validate(); err != nil {
		return err
	}
	return t.fresh(what+" "+m.ID, m.Seq)
}

// sequencedWrite records a companion write carrying a transaction-allocated
// sequence: it satisfies the semantic-write rule and joins the TargetCall
// sharing check (FR-CALL-001).
func (t *tx) sequencedWrite(seq uint64) {
	t.markSequenced()
	t.semSeqs = append(t.semSeqs, seq)
}

// deferCheck registers a reference check that must hold when the
// transaction commits rather than at the write, for records that name each
// other and are inserted as one bundle (the proof -> satisfying transition
// and retrieval result/projection/event references). A failing check aborts
// Update, so nothing commits.
func (t *tx) deferCheck(check func() error) { t.deferred = append(t.deferred, check) }

func (t *tx) runDeferred() error {
	for _, check := range t.deferred {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

// page collects one page of records from refs, which yields (Seq, ID)
// references strictly after the page cursor. load returns the record and
// whether it is visible to the reader; hidden records are skipped before
// the limit is applied, so More never reveals them.
func page[T any](p store.Page, refs iter.Seq[seqRef], load func(id string) (T, bool)) (store.ResultPage[T], error) {
	if p.Limit <= 0 {
		return store.ResultPage[T]{}, invalid("page limit must be positive")
	}
	var out store.ResultPage[T]
	for ref := range refs {
		rec, visible := load(ref.id)
		if !visible {
			continue
		}
		if len(out.Records) == p.Limit {
			out.More = true
			break
		}
		out.Records = append(out.Records, rec)
		out.Next = store.Cursor{Seq: ref.seq, ID: ref.id}
	}
	return out, nil
}

func cursorRef(c store.Cursor) seqRef { return seqRef{seq: c.Seq, id: c.ID} }

// visibleAll marks every loaded record visible, for internal reads without
// an access filter.
func loadAll[K comparable, V any](tb *table[K, V], key func(string) K) func(string) (V, bool) {
	return func(id string) (V, bool) { return tb.get(key(id)) }
}

func ident(id string) string { return id }

// sortedUnique reports whether keys are strictly increasing.
func sortedUnique(keys []string) bool {
	return slices.IsSortedFunc(keys, func(a, b string) int {
		if a < b {
			return -1
		}
		return 1
	}) && len(slices.Compact(slices.Clone(keys))) == len(keys)
}
