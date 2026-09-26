# SQLite store notes

## Missing records and compare-and-swap

The store contract's general compare-and-swap paragraph says a missing record
reads as version 0, so an update with an expected value above 0 conflicts.
The conformance suite and memory store instead return `ErrNotFound` for
update-only methods (`UpdateItem`, `UpdateObligationVersion`, and
`UpdateCall`) when their target is missing. SQLite follows the suite.

Recommendation: limit the version-0 wording to the create-or-replace methods
(`PutTask` and `PutConversation`), and state explicitly that update-only
methods return `ErrNotFound` for a missing target.
