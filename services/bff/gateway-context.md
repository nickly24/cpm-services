# BFF → Tests request identity

Browser test requests perform one live Auth `/v1/auth/me` check at the BFF. With
`BFF_TESTS_CONTEXT_KEY` configured on both services, BFF signs the verified user
snapshot and Tests does not introspect that same browser operation again.
Direct bearer calls to Tests continue to introspect Auth.

The context uses `X-CPM-Gateway-Context` (base64url JSON) and
`X-CPM-Gateway-Signature` (HMAC-SHA256 over the encoded context). Version 1 has
issuer `cpm-bff`, audience `tests`, Unix issue/expiry times (maximum lifetime 30
seconds), exact HTTP method and request URI including query, SHA256 of the exact
body, and the verified Auth user. BFF never forwards browser context/signature
headers. Invalid, expired, duplicate or unconfigured assertion headers are
rejected; they never fall back to a different identity mechanism.

Tests remains the owner of object authorisation: account, organisation,
permissions, current division and immutable attempt/submission constraints.
Individual attempts/submissions are fenced to the BFF-selected cabinet even for
organisation admins. This replaces the previous BFF full-attempt preflight.
Auth validates the active selected organisation/division at request entry. Tests
reuses that signed check, so commits/finalisation do not ask Core to recheck it.
Direct bearer calls retain the live Core scope check; business reference metadata
needed for starting tests and lists is fetched in bounded batches.

Commit acknowledgement adds `server_answer_count` and RFC3339 `server_time`.
The browser uses these fields without reloading the complete attempt after each
commit batch. Existing `acknowledged_commit_ids` and `revision` remain unchanged.

For local Dev, `./dev/dev gateway-prepare` provisions a dedicated random key in
`dev/local/gateway-context.env`, mode0600, without printing it or rotating an
existing key. Both Compose services read the file. The native `./dev/bff server`
launcher validates and loads private credential files without shell sourcing.
Update BFF and Tests together when enabling this contract. A deployment without
the optional key keeps the previous bearer/preflight behaviour.

Trusted assertions are for authenticated private service transport. Production
must keep this credential private and isolate/encrypt service traffic. In-flight
requests retain the identity checked at entry; subsequent browser requests see
revocation and cabinet changes through the live Auth check.
