package policy

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// TestP3_33_AuthoredKnowledgeOutlivesSourceExpiryButRawProjectionDoesNot
// closes the P3-42 table row "semantic knowledge versus raw projection"
// (ADR8:1256), semantic side. P3-33: authored derived facts may outlive their
// source's expiry, while raw retained/opaque/projection representations must
// recheck their source/lease dependencies. Here that distinction is
// Eligibility's Representation input: the authored fact keeps its own
// lifetime when the evidence it cites has expired, but the raw copy of the
// same expired evidence is representation-ineligible, and a live lease
// cannot rescue it — representation expiry is not an admission boundary a
// lease can bridge. Pure-policy row: Eligibility's only inputs are the item
// and one validated snapshot, so no store is involved; the store-side
// dependency recheck of raw projections is the cited test's half
// (TestDerivedRepresentationRetainsProjectionLeaseAndNestedCoverage).
func TestP3_33_AuthoredKnowledgeOutlivesSourceExpiryButRawProjectionDoesNot(t *testing.T) {
	authored, base, p := eligibilityFixture()
	// An authored derived fact, not a copy: ordinary WORKING knowledge whose
	// cited evidence has since expired.
	authored.Kind, authored.Generation = domain.KindFact, domain.GenerationWorking
	authored.ID, authored.Seq = "authored", base.Seq
	// The raw representation of the same expired evidence.
	raw := authored
	raw.ID = "raw-copy"
	raw.Role = domain.RoleProjection
	raw.Kind = domain.KindToolResult

	snap := func(id string) EligibilitySnapshot {
		s := base
		s.Item = domain.ItemRevisionRef{ItemID: id, Version: 1}
		return s
	}

	// Authored knowledge with an expired cited source stays ordinarily
	// selectable: authored content owns its lifetime.
	s := snap(authored.ID)
	if r := Eligibility(authored, s, p, s.Task.TurnID); !r.Access || !r.OrdinaryTemporal || !r.NewSelection || r.TemporalReason != ReasonAllowed {
		t.Fatalf("authored fact inherited source expiry: %+v", r)
	}

	// The raw projection of the same expired evidence is ineligible with the
	// representation reason on every output, before any lease is consulted.
	s = snap(raw.ID)
	s.Representation = domain.ExpiryExpired
	if r := Eligibility(raw, s, p, s.Task.TurnID); r.OrdinaryTemporal || r.NewSelection || r.LeaseAdmission ||
		r.TemporalReason != ReasonRepresentation || r.LeaseReason != ReasonRepresentation || r.SelectionReason != ReasonRepresentation {
		t.Fatalf("raw projection of expired evidence admitted: %+v", r)
	}

	// Control: the same raw projection with a live source is selectable, so
	// the refusal above came from the inherited expiry, not the role.
	s = snap(raw.ID)
	if r := Eligibility(raw, s, p, s.Task.TurnID); !r.Access || !r.NewSelection {
		t.Fatalf("live-source projection refused: %+v", r)
	}

	// A genuinely live lease on the expired representation does not rescue
	// it: LeaseAdmission must stay false with the representation reason.
	_, lease, _ := leaseFixture()
	lease.Source = domain.ItemContentRef{ItemID: raw.ID, ContentHash: raw.ContentHash}
	s = snap(raw.ID)
	s.Representation = domain.ExpiryExpired
	s.Leases = []domain.RetrievalLease{lease}
	if r := Eligibility(raw, s, p, s.Task.TurnID); r.LeaseAdmission || r.OrdinaryTemporal || r.SelectionReason != ReasonRepresentation {
		t.Fatalf("lease rescued an expired representation: %+v", r)
	}
}
