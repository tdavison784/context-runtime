// Package obligation implements obligation declaration, matcher evaluation,
// status transitions, applicability proofs, resource reporting, typed
// observations, and proof invalidation (FR-OBL-001..006, FR-REL-007, P3-12..23).
//
// It is a trust boundary. Every rule fails closed:
//
//   - Only registered matcher versions run, over validated typed observations
//     and authoritative resource state; no caller names a matcher, outcome, or
//     text as proof. An unknown version is never replaced by a newer one.
//   - A positive matcher transition needs a live grant naming the exact
//     obligation version and matcher version, authorized at the mutation's
//     allocated sequence. AGENT, TOOL, and RETRIEVED_CONTENT never change status.
//   - Unknown or missing resource freshness is not applicable; it never
//     preserves resource-bound satisfaction.
//   - Satisfaction is published only when the obligation's boundary lies
//     within every evidence and applicability boundary.
//   - Outward errors are fixed and carry no hidden IDs or counts.
//   - A multi-record effect commits entirely in one transaction or not at all.
package obligation
