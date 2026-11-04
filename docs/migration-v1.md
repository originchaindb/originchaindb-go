# Migrating to API v1

This guide applies to the 1.0 source candidates. Package publication is paused;
install the reviewed source using this repository's README. A registry install
can select an earlier package with a different API.

1. Copy the complete database address from Connect into `OC_URL`, and use the
   database access token as `OC_TOKEN`. Remove application code that builds a
   tenant path from a host or token.
2. Use the resource entry points in the [API v1 reference](api/v1/README.md).
   Run the README's first query before moving your application queries.
3. Read SQL results as statement envelopes. Missing write counts remain unknown;
   a batch can partially succeed. Preserve nested results when handling failures.
4. Preserve exact numbers and numeric strings. Use the optional model conversion
   API with native types or an explicit validating decoder.
5. Reuse clients according to the language's concurrency/ownership guidance.
   Add a credential provider or local observer only when your application needs it.
6. Run the complete catalog example in a development database, then validate
   your own schemas, permissions, query behavior and failure handling.

Static-token construction remains supported. Python and TypeScript retain their
existing compatibility helpers; consult their deprecation messages before
changing older call sites. The native consumer tests preserve the common v1 API.

## Versioning policy

Source candidates are identified by their exact Git commit. A candidate version
does not imply registry publication or production server compatibility.
Once stable 1.0 packages are published, patch releases preserve public signatures
and behavior except documented bug/security fixes; minor releases add compatible
features. Removing supported methods or changing wire/result semantics requires
a major version, migration notes, and updated cross-language contract review.

Keep existing consumer and regression tests when extending an API. CI must build
the previous core usage alongside the new usage and run both HTTP fixtures.
Deprecations need a documented replacement and removal version. Never infer a
safe retry from an HTTP method, a SQL string, or a client-supplied idempotency key;
the server must explicitly support deduplication for the operation.
