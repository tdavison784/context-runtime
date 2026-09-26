// Package contextruntime is a provider-aware context runtime for LLM agents.
// It keeps audit history, semantic state, and the context sent to each model
// call separate, and decides per call what the model needs and how to give it
// to the target provider. See SDD.md for the V1 contract.
//
// Phase 1 exposes the domain vocabulary and machine-checkable errors; the
// Runtime interface (SDD section 8) arrives with the phases that implement it.
package contextruntime
