# OriginChainDB for Go

The official Go SDK for working with OriginChainDB through SQL, rows,
vector search, full-text search, and graph relationships. Use one database client
for these operations, with native Go types and conventions.

[![CI](https://github.com/originchaindb/originchaindb-go/actions/workflows/ci.yml/badge.svg)](https://github.com/originchaindb/originchaindb-go/actions/workflows/ci.yml)

**Requirements:** Go 1.23+. **Package:** `github.com/originchaindb/originchaindb-go`.

> This README describes the **1.0.0-rc.1 source candidate**. It has not been
> published to package registries. Follow the source installation below; an older
> registry package may expose a different API.

[Install](#installation) | [Connect](#connect-and-run-your-first-query) |
[Examples](#product-catalog-example) | [Results](#understanding-sql-results) |
[Errors](#handling-errors) | [Source](#source-layout)

## Installation

From your application's Go module, install the verified source revision:

```sh
go get github.com/originchaindb/originchaindb-go@dde36c7021e687ce6852a63554ffcbdb1983fa7c
```

For a new application, first create a directory and run `go mod init example.com/myapp`.
The SDK uses Go's standard library and adds no third-party runtime dependencies.

Pin a reviewed source commit in your application or build process. See the
[changelog](CHANGELOG.md) before updating.

## Connect and run your first query

Copy the complete database address from **Connect** into `OC_URL`, and the
database access token into `OC_TOKEN`. The SDK sends the token as a bearer
credential. It uses the address as supplied; do not add or derive a tenant path.

macOS/Linux shell:

```sh
export OC_URL="<database HTTPS address from Connect>"
export OC_TOKEN="<database access token>"
```

PowerShell:

```powershell
$env:OC_URL = "<database HTTPS address from Connect>"
$env:OC_TOKEN = "<database access token>"
```

Replace the placeholders, keep credentials out of source control, and use a
secret manager for deployed applications. HTTPS is required except for local
loopback development. SQL tools use a separate database login from Connect.

For a newly created token, wait for its database access state to become **active**.
Its linked database identity still controls table access, row policies and masks.
See [token setup, rotation and revocation](docs/authentication.md).

Save this as `main.go` in your application and run `go run .`.

```go
package main

import (
	"context"
	oc "github.com/originchaindb/originchaindb-go"
	"log"
)

func main() {
	db, err := oc.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	result, err := db.SQL.Run(context.Background(), "SELECT $1 AS value", 7)
	if err != nil {
		log.Fatal(err)
	}
	log.Println(result.Rows)
}
```

The query needs no tables. Its returned row contains `value` equal to `7`.
`$1` binds the first parameter; `$2` binds the second. Bind values instead of
interpolating application input into SQL. Parameters do not substitute table or
column names; those identifiers must come from trusted application code.

## Product catalog example

The complete [catalog program](examples/catalog/main.go) creates a small product catalog and
demonstrates the same sequence shown below. It uses the included
[products.toml](examples/products.toml) schema and writes two rows plus search
data under `sdk_examples.products`. **Run it against a development database.**
Your token needs permission to register schemas and read/write the relevant data.

Run from the SDK checkout root after installation and setting the two variables:

```sh
git clone https://github.com/originchaindb/originchaindb-go.git
cd originchaindb-go
go run ./examples/catalog
```

The sections below are excerpts from that complete program. Go snippets belong inside a function returning `error`, with `db` and `ctx` initialized as in the complete example.
Required imports are `context`, `fmt`, `os`, and the SDK as `oc`; the complete file includes them.

### Register the schema

The schema declares string columns `id`, `name`, `description`, and `related_to`.
`id` is the primary key. The `related` relation connects a product to the product
named by `related_to`. Registration reads the TOML file; it does not require
you to construct a schema request manually.

```go
schema := "sdk_examples.products"
toml, err := os.ReadFile("examples/products.toml")
if err != nil {
	return err
}
if _, err := db.Schemas.Register(ctx, string(toml)); err != nil {
	return err
}
```

### Write and read rows

Create the referenced product first, then the product pointing to it. `put`
writes the supplied row; `get` retrieves it by primary key. The example prints
the row for `p1`, named `Widget`.

```go
_, err = db.Rows.Put(ctx, schema, map[string]any{
	"id": "p2", "name": "Gadget", "description": "A red gadget",
})
if err != nil {
	return err
}
_, err = db.Rows.Put(ctx, schema, map[string]any{
	"id": "p1", "name": "Widget", "description": "A small blue widget", "related_to": "p2",
})
if err != nil {
	return err
}
product, err := db.Rows.Get(ctx, schema, "p1")
if err != nil {
	return err
}
fmt.Println(product)
```

For multiple rows, the rows namespace also provides a batch-put operation.
Treat its response as a batch report, not as a promise of transaction atomicity.

### Query with SQL

Use `sql.run` (with the language's casing) for a complete statement result.
`execute` is an alias; `query` is intended for SELECT results.

```go
result, err := db.SQL.Run(ctx,
	"SELECT id, name FROM sdk_examples.products WHERE id = $1", "p1")
if err != nil {
	return err
}
for _, row := range result.Rows {
	fmt.Println(row["id"], row["name"])
}
```

Use the same parameter binding for INSERT, UPDATE, and DELETE. Read the
server-reported write count rather than assuming a successful request changed a row.

### Search by vector similarity

Store an embedding with its product ID, then request up to five nearest hits.
The default metric is cosine. These four numbers are a small illustrative vector;
generate real embeddings with your chosen model, and use that same model and
dimension for stored vectors and queries. The SDK does not generate embeddings.

```go
embedding := []float64{0.1, 0.2, 0.3, 0.4}
if _, err := db.Vector.Put(ctx, schema, "p1", embedding, map[string]any{"category": "tools"}); err != nil {
	return err
}
hits, err := db.Vector.Search(ctx, schema, embedding, oc.VectorSearchOptions{K: 5})
if err != nil {
	return err
}
for _, hit := range hits {
	fmt.Println(hit.ID, hit.Score)
}
```

Each hit exposes an ID, score, optional metadata, and the original hit. Do not
assume the score is a probability; its interpretation depends on the metric.

These calls store vectors per table. For named indexes with namespaces, batch
upserts, metadata filters, sparse, keyword and hybrid search, see
[Vector indexes](#vector-indexes).

### Search text

Index the description explicitly, then use ranked BM25 search. Search defaults
to ten hits; the example asks for five. Boolean and phrase modes, highlighting,
fuzzy matching, and facets are available through the language's search options.

```go
if _, err := db.FTS.Index(ctx, schema, "description", "p1", "A small blue widget"); err != nil {
	return err
}
matches, err := db.FTS.Search(ctx, schema, "description", "blue widget", oc.FTSSearchOptions{K: 5})
if err != nil {
	return err
}
for _, hit := range matches.Hits {
	fmt.Println(hit.DocID, hit.Score)
}
```

The result contains `hits`, optional `facets`, and the original response.
Hits have a document ID and score; unranked modes use zero for the score.
Writing a row does not imply this example's separate full-text or vector index
entry has been updated: update the relevant index when your application changes data.

### Follow graph relationships

The schema's `related` relation and the `p1.related_to = p2` row value create the
connection used here. The result includes the neighbor `p2`.

```go
neighbors, err := db.Graph.Neighbors(ctx, schema, "p1", "related")
if err != nil {
	return err
}
fmt.Println(neighbors)
```

The graph namespace also supports breadth-first traversal and Cypher queries.
Cypher parameters bind **by name**; SQL parameters bind **by position**.
See the [API guide](docs/api.md) and source links below for exact signatures.

## Bulk writes and paginated reads

For larger datasets, `WriteBatches` sends bounded batches and yields an acknowledgment
for each input range. `Scan` reads rows lazily in ordered pages; `Pages` also
exposes a continuation value for resumable processing. These operations use the
same limits and failure rules across all SDKs.

```go
for batch, err := range db.Rows.WriteBatches(ctx, schema, rows, oc.BatchOptions{BatchSize: 2}) {
    if err != nil { return err }
    fmt.Println(batch.Offset, batch.Count, batch.Inserted)
}
```

This excerpt uses `db`, `schema` and the lazy `rows` producer from the
[complete example](examples/bulk-and-scan/main.go). See the [bulk and pagination guide](docs/bulk-and-pagination.md)
for setup, reads, checkpoints, cancellation and recovery. Failed writes are never
replayed automatically; paginated reads require a unique immutable string key.

## Vector indexes

`db.VectorIndexes` holds the record operations on vector indexes. An index
holds namespaces, and a namespace holds records. A record has an `id` and any
of a dense vector (`values`), a sparse vector (`sparse_values`), flat
`metadata` and declared `text` fields. Every call takes the index by its name
or its `index_id`, and record calls always name a namespace
(`oc.VectorDefaultNamespace`, `"__default__"`, is the default one). These
routes are separate from the per-table `db.Vector` calls; the two never share
data.

```go
vi := db.VectorIndexes

// Create an index. Metric is "cosine", "dot", "l2" or "manhattan".
_, err := vi.CreateIndex(ctx, "products", oc.CreateIndexOptions{Dimension: 4, Metric: "cosine", Sparse: true,
	TextFields: []oc.VectorTextField{{Name: "body", Analyzer: "standard_en_v1"}}})
if err != nil { return err }
for index, err := range vi.AllIndexes(ctx, oc.ListIndexesOptions{}) {
	if err != nil { return err }
	fmt.Println(index["name"], index["state"])
}

// Upsert replaces the whole record. The namespace is created on first write.
records := []map[string]any{
	{"id": "p1", "values": []float64{0.1, 0.2, 0.3, 0.4},
		"metadata": map[string]any{"category": "tools", "price": 12.5},
		"text":     map[string]any{"body": "steel claw hammer"}},
	{"id": "p2", "values": []float64{0.4, 0.3, 0.2, 0.1},
		"sparse_values": map[string]any{"indices": []int{3, 9}, "values": []float64{0.2, 0.5}},
		"metadata":      map[string]any{"category": "garden"}},
}
if _, err := vi.Upsert(ctx, "products", "catalog", oc.FromSlice(records), oc.UpsertOptions{}); err != nil {
	return err
}

// Dense search with a metadata filter.
res, err := vi.Search(ctx, "products", "catalog", oc.IndexSearchOptions{
	K: 5, Vector: []float64{0.1, 0.2, 0.3, 0.4},
	FilterExpr: map[string]any{"category": map[string]any{"$eq": "tools"}, "price": map[string]any{"$lt": 20}},
	IncludeMetadata: oc.Ptr(true),
})
if err != nil { return err }
hits, _ := res["hits"].([]any)
for _, hit := range hits {
	h, _ := hit.(map[string]any)
	fmt.Println(h["id"], h["score"], h["metadata"])
}

// Hybrid search: dense plus keyword (BM25), fused by reciprocal rank. Search by
// the stored vector of a record with RecordID, or send a Sparse query.
res, err = vi.Search(ctx, "products", "catalog", oc.IndexSearchOptions{
	K: 5, Vector: []float64{0.1, 0.2, 0.3, 0.4},
	Lexical: map[string]any{"field": "body", "query": "hammer"},
})
if err != nil { return err }

// Fetch keys records by id and lists the ids it did not find.
got, err := vi.Fetch(ctx, "products", "catalog", []string{"p1", "p9"}, oc.FetchOptions{IncludeValues: oc.Ptr(true)})
if err != nil { return err }
fmt.Println(got["records"], got["missing"])
if _, err := vi.Update(ctx, "products", "catalog", "p1", oc.UpdateOptions{MetadataMerge: map[string]any{"price": 11.0}}); err != nil {
	return err
}
for id, err := range vi.AllIDs(ctx, "products", "catalog", oc.ListIDsOptions{}) {
	if err != nil { return err }
	fmt.Println(id)
}

// Deleting by filter is a background job; poll it until state is not queued or running.
job, err := vi.DeleteByFilter(ctx, "products", "catalog", oc.DeleteByFilterOptions{
	FilterExpr: map[string]any{"category": map[string]any{"$eq": "garden"}},
})
if err != nil { return err }
jobID, _ := job["job_id"].(string)
status, err := vi.GetJob(ctx, "products", jobID)
if err != nil { return err }
fmt.Println(status["state"])
```

Use the fragment in a function returning `error`; `ctx` is a `context.Context`
and `oc` imports this module.

Things to know:

- **Batching.** The engine accepts at most 1,000 records and 2,000,000 bytes
  per upsert request. `Upsert` reads its input lazily, sends it in requests
  under both bounds, in order, each record exactly once, and returns an
  `UpsertResult` (`UpsertedCount`, `NamespaceCreated`, `Batches`, `Receipts`).
  Each request commits on its own. A failure returns a `*BatchWriteError` with
  the input range that was not acknowledged (`Offset`, `Count`, and `Outcome`
  `not_sent` or `unknown`); every record before `Offset` is written and counted
  in the result, and `Unwrap` gives the cause. Nothing is sent again
  automatically. `UpsertBatches` yields one `UpsertBatchResult` per
  acknowledged request instead. `oc.FromSlice` turns a slice into input.
- **Search.** `K` is required. One query leg is a semantic (dense or sparse)
  or keyword search; two or more legs are a hybrid search. `Quality` is
  `"fast"`, `"high_recall"` (the default) or `"exact"`. Scores are always
  higher-is-better; `res["completeness"]` says whether an approximate leg may
  have missed records. Search returns metadata only with `IncludeMetadata`
  set to `oc.Ptr(true)`; `Fetch` returns it unless you set it to false.
- **Filters.** `FilterExpr` takes operators such as `$eq`, `$ne`, `$in`,
  `$gt`, `$exists`, `$and` and `$or`; `Filter` takes a plain equality map.
  Set at most one.
- **Updates and deletes.** `Update` changes one record in place and keeps the
  fields it does not name. `UpdateByFilter` and `DeleteByFilter` run as
  background jobs; `DryRun` only reports how many records match. Calling
  `DeleteNamespace` or `DeleteAll` is the confirmation the engine asks for.
- **Paging.** `AllIndexes`, `AllNamespaces`, `AllIDs` and `AllRecords` hold one
  page in memory and do not prefetch, starting at the options' `Cursor`. They
  are not snapshots.
- **Results.** Each call returns the server's JSON object as a
  `map[string]any`. Numbers are `json.Number` and fields this SDK does not
  know are kept.
- **Errors and retries.** Failures are `*oc.Error`. Branch on `Code` plus
  `Reason` (the envelope's `error.details.reason`), not on `Status` alone;
  `Details` holds the rest, `Message` the server's message, and `Retryable`
  says whether the same request can succeed later. A database that does not
  serve these operations answers with a 501 that matches
  `errors.Is(err, oc.ErrRecordAPINotEnabled)`. With `Config.MaxRetries` set,
  reads are retried; writes never are.

## Understanding SQL results

Rows are `[]map[string]any`; JSON numbers are `json.Number`. SQL returns a result object rather than only an array:

| Member | Meaning |
| --- | --- |
| `Kind` | Statement kind, such as select, insert, update or delete |
| `Rows` | Returned rows, including RETURNING rows when the server supplies them |
| `Columns` | Column names, when supplied |
| `ColumnTypes` | Column type metadata, when supplied |
| `RowsAffected` | Server-reported write count; missing means unknown |
| `RowsBuffered` | Buffered count, when supplied; it is not proof of a committed write |
| `Results` | Individual results for a multi-statement response |
| `Raw` | Original response, including additional server fields |

Write counts use `*int64`; `nil` means the server did not report
a count. Empty rows are not evidence of a failed statement: a write may return
only counts. Conversely, a malformed response is an error, not an empty success.

Numeric and temporal **strings** remain strings. Convert them deliberately in
your application when you know the column type. Unsupported write-count ranges
raise a protocol error instead of silently rounding.

Multi-statement batches can partially succeed. A failed batch exposes the
available statement results through its error. Do not assume batches are atomic
or replay an entire batch without checking which statements completed.

## Timeouts and client lifetime

The default request timeout is 60 seconds. Share a client across goroutines
and pass your application's context to every call:

```go
db, err := oc.FromEnv(oc.WithTimeout(10 * time.Second))
if err != nil { return err }
defer db.Close()
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
result, err := db.SQL.Run(ctx, "SELECT $1 AS value", 7)
if err != nil { return err }
fmt.Println(result.Rows)
```

This fragment belongs in a function returning `error`; import `context`, `fmt`,
and `time`. `Close` releases idle connections; cancel the context to interrupt an
active call.

Requests are **not retried automatically by the SDK by default**, and redirects
are not followed. `Config.MaxRetries` opts the [vector index](#vector-indexes)
read operations into retries; writes are never sent twice. A timeout or disconnect can happen after a write committed.
Canceling the local wait does not prove rollback. Only retry an operation when
your application can safely reconcile or repeat it.

## Handling errors

```go
result, err := db.SQL.Run(ctx, "SELECT $1 AS value", 7)
if err != nil {
    var failure *oc.Error
    if errors.As(err, &failure) {
        fmt.Println(failure.Kind, failure.Status, failure.RequestID)
        if failure.Partial != nil {
            fmt.Println("Partial batch:", len(failure.Partial.Results))
        }
    }
    return err
}
fmt.Println(result.Rows)
```

Import `errors` and `fmt`, and use the fragment in a function returning `error`.
`Kind` distinguishes HTTP, transport, timeout, protocol, and batch failures.
For a batch failure, `Run` also returns the available partial result alongside the error.
When the server answers with a structured error envelope, `Code`, `Reason`,
`Details`, `Message` and `Retryable` carry its fields; `Error()` still leaves
out the server's text.

| Symptom | What to check |
| --- | --- |
| 401 or 403 | Token validity, expiry, database access and operation grants |
| 404 | Complete database address, schema name and resource ID |
| 400 or 422 | SQL syntax, parameter order, schema fields and vector dimensions |
| 429 | Capacity/rate limits and any server-provided retry timing |
| Timeout or 5xx after a write | Check the outcome before retrying |
| Empty search results | Indexed data, query terms, dimensions, filters and feature availability |

Include the SDK version and request ID in a support report. Redact credentials
and application data. Available database features and token permissions still
determine which operations can run.

## Application integration

The [API v1 reference](docs/api/v1/README.md) covers typed row conversion,
rotating credentials, local request diagnostics, native HTTP configuration,
and cancellation. The [complete integration example](examples/integration/main.go) shows
these features together using a read-only query. See the
[migration guide](docs/migration-v1.md) when upgrading an earlier application.

## Source layout

The Go package lives in the repository root. A `src/` directory is not required.

| Path | Purpose |
| --- | --- |
| [client.go](client.go) | Client, configuration and HTTP transport |
| [resources.go](resources.go) | Schema, row, vector, full-text and graph services |
| [vector_indexes.go](vector_indexes.go), [retry.go](retry.go) | Vector index record operations and read retries |
| [sql.go](sql.go) | SQL operations and results |
| [search.go](search.go), [vector.go](vector.go) | Search result types |
| [examples/](examples/) | Quickstart, product catalog and schema |
| [tests/contract/run.py](tests/contract/run.py) | Shared offline HTTP conformance fixture |
| [docs/api.md](docs/api.md), [docs/contract.md](docs/contract.md) | Native API notes and shared behavior |

All eight SDKs share the same core concepts and HTTP contract. Method casing,
JSON types and concurrency follow each language. Historical Python/TypeScript
helpers are broader than this common core; do not assume every advanced helper
exists in every SDK.

## Development and support

From the checkout root:

```sh
go test ./...
go vet ./...
python tests/contract/run.py -- go run ./tests/consumer
```

The contract runner requires Python 3.10+ for testing only and uses a local
fixture; it does not require database credentials. Application runtime
requirements are listed at the top. Live examples use your selected database.

Read [Contributing](CONTRIBUTING.md) before sending a change. Report bugs through
[GitHub issues](https://github.com/originchaindb/originchaindb-go/issues); report security concerns
privately as described in [Security](SECURITY.md). See [LICENSE](LICENSE) for MIT terms.

See [quality and build verification](docs/quality.md) for benchmarks, API compatibility checks, package installation checks and signed build provenance.
