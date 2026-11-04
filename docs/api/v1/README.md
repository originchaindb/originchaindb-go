# API v1: Go

This reference describes the 1.0 source candidate's shared core and application
integration APIs. See the [installation and data examples](../../../README.md),
[migration guide](../../migration-v1.md), and [source](../../../client.go)
for complete signatures, native types and language-specific helpers.

## Core operations

| Resource | Entry points |
| --- | --- |
| SQL | `SQL.Run / SQL.Query / SQL.Execute` |
| Schemas | `Schemas.List / Schemas.Get / Schemas.Register` |
| Rows | `Rows.Get / Rows.Put / Rows.PutBatch`  `WriteBatches / Pages / Scan` |
| Vectors | `Vector.Put / Vector.Search / Vector.Delete` |
| Full-text search | `FTS.Index / FTS.Search` |
| Graph | `Graph.Query / Graph.Neighbors / Graph.BFS` |
| Usage and query metrics | `Usage / QueryMetrics` |

`run`/`RunAsync` accepts any supported SQL statement and returns the complete
statement envelope. `query`/`QueryAsync` requires a SELECT result. `execute` is
an alias for the complete result path. Bind values with positional parameters;
identifiers must come from trusted application code.

Rows use a schema name and primary key. Schema registration accepts TOML text.
Vector search returns IDs and scores; full-text search returns hits plus metadata
when supplied by the server. Graph operations use the relations declared in the
schema. The README has complete setup and query examples for each resource.

## Bounded iteration

`WriteBatches` yields BatchResult (offset, count, inserted); `Pages` yields RowPage
(rows, `NextCursor`); `Scan` yields individual rows. Their iterators are lazy,
sequential, and do not automatically retry writes. `BatchWriteError` identifies an
unconfirmed input range and its original cause. Read the [bulk and pagination
guide](../../bulk-and-pagination.md) for exact limits, supported key types,
cancellation and checkpoint semantics.

## Configure an application

`NewClient(Config{URL: ..., TokenProvider: ..., OnRequest: ...}, options...)`

Existing static-token constructors remain supported. Choose one credential source.
Copy the entire database address from Connect; no tenant path is inferred.

Use WithTransport(http.RoundTripper) for custom TLS roots, proxy settings or tests. The SDK disables HTTP redirects. Close releases idle connections through the configured transport; active calls are unaffected.

The [complete integration example](../../../examples/integration/main.go) uses a read-only
parameterized query, typed products, a token provider and a local observer. It
needs `OC_URL` and `OC_TOKEN`, but no schema setup or embedding service:

```sh
go run ./examples/integration
```

## Rotating credentials

A TokenProvider receives context.Context and returns (string, error). It must honor context cancellation. Providers and observers must be safe for concurrent calls.

The SDK validates every returned token and sends it unchanged as a bearer value.
No prefix is required. It does not implement a refresh protocol or automatically
retry after a 401/403. The next application call resolves the provider again.
Provider failures do not send an HTTP request and do not expose the provider's
exception message in the SDK's error text. Callbacks must not log credentials.

The example reads the process environment on each call. External secret rotation
does not update an existing process environment automatically; a deployed service
should return the current value from its own secret manager/cache.

## Typed rows

`DecodeRows[Product](result)`

encoding/json maps fields using json tags. Interface values retain json.Number. Native rules for missing and unknown fields apply; validate application requirements explicitly. RowDecodeError.Row is zero-based and Unwrap exposes the cause.

Conversion makes no network request, preserves the raw result, and returns no
partial list on failure. It does not invent missing execution counts or coerce
exact decimal strings through floating point. Application-selected conversions
can change precision, so choose model types appropriate to your schema.

## Local request observation

The optional observer receives method, operation family, elapsed time
(`Duration (time.Duration)`), HTTP status, server request ID, and error category.
It receives no database URL, schema name, SQL text, query values, headers, response
body or credentials. It does not enable a telemetry exporter. Forward these
fields into your existing logs, metrics or tracing system when needed.

Observers run once per logical call. They should be short and nonblocking;
ordinary callback failures are isolated from the database result. Status is
absent (zero in Go) if no response headers arrived. A successful HTTP event may
be followed by a result-decoding, batch or model-conversion failure.

Common failure categories include authentication, permission, rate_limit,
timeout, canceled, transport, protocol, and credential. Native error types and
fields remain available: `*Error with Kind credential; *RowDecodeError.Row`. The README explains HTTP errors,
partial batches, and request IDs.

## Cancellation and ownership

Every operation accepts context.Context. The request deadline includes credential resolution and HTTP work. New, FromEnv, WithTimeout and WithTransport remain available.

Stopping the local wait does not prove that a server-side write was canceled.
Automatic SDK retries are disabled by default. Inspect a failed batch's partial
result before deciding how the application should recover.

## Compatibility checks

CI compiles/runs the native consumer, shared 24-request HTTP contract, nine-request
resilience contract, 21-request bounded iteration contract, public API baseline, and applicable language tests. Those consumers exercise the
public core API and fail when a required method, type or behavior is removed.
The shared fixtures are pinned to the [organization contract](https://github.com/originchaindb/.github/tree/073b0396db55c903ca114f15fab8cf83c4b92e3d/sdk-contract/v1).
Changes to either fixture must be coordinated across all eight SDKs.

These checks cover the shared core. Changes to additional helpers also require
their native tests and API review. Fixtures model authentication failures; they
do not certify a deployed token lifecycle or an untested server version.

See [live verification](../../live-testing.md) for the manually dispatched database check.
