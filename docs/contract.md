# SDK behavior contract

Contract version: **1**. The public clients are handwritten. The same black-box
HTTP contract tests are checked into every language repository.

## Connection

`OC_URL` is the complete HTTPS database address copied from Connect. Its path is
opaque; clients append operation paths without inferring a tenant or instance ID.
`OC_TOKEN` is the bearer access token. HTTP is accepted only for loopback development.
Redirects are not followed. Database tools use the separate PostgreSQL login.

Clients are reusable. The default request timeout is 60 seconds. Ruby applies its
timeout to connection/read/write inactivity; other clients apply their native
request deadline. Ruby and PHP clients serialize work per instance; use a client
per concurrent worker. Go, Rust, Java, .NET and the async clients support concurrent calls.

There are **no SDK retries by default**. A timeout, disconnect, or 503 can follow a
committed write. Cancellation stops the local wait; it does not prove rollback.
Python and TypeScript retain explicit retry options for existing integrations.

## Common operations

| Namespace | Operations |
| --- | --- |
| SQL | Run any statement or batch; execute alias; SELECT query |
| Schemas | List, get, register TOML |
| Rows | Get, put, put batch |
| Vector | Put, search, delete |
| Full-text | Index; BM25, boolean and phrase search; highlighting, fuzzy search, facets |
| Graph | Cypher query with named parameters, neighbors, BFS |
| Diagnostics | Usage and query metrics |

Method casing follows each language. SQL values bind positionally through `$1`,
`$2`, and so on. Cypher values bind by name. Resource names are encoded as individual
path segments. Server capabilities and token grants determine which operations
are available on a particular database.

## Results and errors

SQL run results retain `kind`, rows, columns, column types, confirmed write counts,
buffered counts, nested statement results, and the original envelope. Missing write
counts mean unknown. A nested batch failure raises/returns an error with partial
results; batches are not assumed atomic.

Vector results consistently expose ID, score, metadata, and the original hit.

Full-text results consistently expose hits and optional facets. Each hit has a
document ID and score; unranked searches use zero. Additional response data remains
available through the result or original envelope.

SQL numeric and temporal strings are preserved. Native arbitrary-precision values
or JSON number representations are used where available. JavaScript returns unsafe
JSON numeric literals as strings. PHP preserves integers beyond the native range as
strings. Counts outside a client's supported integer range raise a protocol error;
they are never silently rounded. See the language reference for native types.

HTTP failures expose the status and request ID when provided. Rate limiting does
not trigger an automatic retry. Protocol errors are distinct from a valid empty
result. Inspect a batch error before retrying any statement.

## Verification

The contract runner uses only a loopback fixture and test credentials. It verifies
actual HTTP requests, exact parameter values, opaque paths, complete SQL results,
partial failures, numeric precision, authentication errors, redirects, and retry counts.
Live acceptance is separate and requires an isolated namespace. A passing fixture
suite is not a claim that every server version or access-token lifecycle is deployed.

## Application integration (contract schema 2)

Each SDK supports an optional credential provider, local request observer, and
row conversion into application models. Providers resolve once per logical call;
an authentication failure never triggers a refresh-and-replay. Observers receive
only method, operation family, elapsed time, status, request ID, and error category.
They do not receive URLs, schema names, SQL, parameters, payloads, or credentials.
Observers describe the HTTP exchange; a later result/model conversion can fail.

Native type systems and serializers determine row conversion rules. Python,
TypeScript, PHP and Ruby accept explicit application decoders; Rust, Go, Java and
.NET also provide native deserialization. Conversion leaves the raw result intact
and reports the zero-based failing row without returning a partial list.

`tests/contract/resilience.py` is shared unchanged across the eight SDKs. Its
nine requests cover expired/revoked token responses, permissions, rate limiting,
an interrupted response after a simulated write, timeout, credential rotation,
observer isolation, and native model conversion. Exact request counts catch
replays and skipped consumers. These fixtures do not certify live token lifecycle
policy. Cancellation follows each language's runtime; synchronous credential
providers must return promptly from an application-managed credential cache.
