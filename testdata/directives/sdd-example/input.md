## Goal
Upgrade Foo to v2 while maintaining backwards compatibility.

## Pinned
- [api] Do not modify exported APIs.
- [tests] {obligation=tests_pass} All tests must pass.
- [architecture] Read docs/architecture.md.

## Working
- Investigating internal/client.go.
- Current issue is TestLegacyClient.

## Remember
- Foo v2 requires context.Context.
- [retry] {kind=decision} Keep the v1 retry policy.

## References
- docs/architecture.md
- go.mod

## Ephemeral ttl=2
- (pasted build output)

## Unpin
- [architecture]
