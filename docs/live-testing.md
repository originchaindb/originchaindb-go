# Live integration checks

The `Live integration` workflow runs the documented application example against
an explicitly configured database. It binds values, resolves the token provider,
converts the result into an application model, and records local request metadata.
It does not create schemas, rows, indexes, or tokens.

Before enabling it, create a GitHub environment named `sdk-integration` with a
required reviewer and a deployment branch rule permitting only `main`. Store
`OC_URL` and `OC_TOKEN` as environment secrets. Use a dedicated integration database
and scoped credential. Do not use repository secrets as a substitute for this
review boundary. Configure the environment before adding its secrets.

Dispatch the workflow from `main` and supply a nonsecret deployment/version label.
Missing credentials fail the job; they do not produce a successful skipped test.
The result records the SDK commit and deployment label. This workflow is separate
from pull-request CI and has no package publication or deployment step.

The shared failure fixture simulates expired/revoked tokens, permission failures,
throttling and interrupted writes. It proves SDK behavior, not the server's token
lifecycle. Certifying issuance, rotation, revocation and expiry requires controlled
server-side fixtures and separate evidence for that deployed server version.

For multimodel verification, run the README's catalog example in a dedicated
namespace/database. It exercises SQL, rows, vector search, full-text search and
graph traversal and writes demonstration data. Do not run it against customer data.
