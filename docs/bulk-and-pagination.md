# Bulk writes and paginated reads

Use `WriteBatches` to consume a producer in bounded batches. Use `Scan` to read
rows lazily, or `Pages` when your application needs a checkpoint between pages.
All eight SDKs use the same limits and failure semantics, with native Go iteration.

## Run the complete example

The [complete program](../examples/bulk-and-scan/main.go) registers [items.toml](../examples/items.toml),
writes five rows under `sdk_iteration.items`, reads them in pages of two, and
resumes after key `b`. Run against a development database using the README's
installation steps and `OC_URL` / `OC_TOKEN`. The token needs schema and row access.
Run from the repository root:

```sh
go run ./examples/bulk-and-scan
```

The write reports are `(offset, count, inserted)` = `(0, 2, 2)`, `(2, 2, 2)`,
`(4, 1, 1)` on a fresh table. The scan prints IDs `a` through `e`; the resumed
scan prints `c`, `d`, `e`. Existing rows may change the server's inserted count.
Use a fresh development namespace when comparing those counts.

## Write a producer without loading it all

The variables below come from the complete program: `db` is the client,
`schema` is `sdk_iteration.items`, and `rows` is a lazy producer.

```go
for batch, err := range db.Rows.WriteBatches(ctx, schema, rows, oc.BatchOptions{BatchSize: 2}) {
    if err != nil { return err }
    fmt.Println(batch.Offset, batch.Count, batch.Inserted)
}
```

| Setting | Default | Allowed range |
| --- | --- | --- |
| `BatchSize` | 1,000 rows | 1–10,000 |
| `MaxBytes` | 1,048,576 bytes (1 MiB) | 2–8,388,608 |

The byte limit covers the compact JSON array of rows, including commas and
brackets. It is a batching bound, not a total process-memory or network-byte
limit. A single oversized row fails locally. JSON encoding temporarily allocates
the row; avoid passing an unbounded single value. Different native JSON encoders
can split the same input at different boundaries while respecting their limits.

The iterator retains one batch and at most one lookahead row when a byte limit
splits a batch. It snapshots input rows, so a producer may reuse a mutable map.
It sends requests sequentially and yields after each acknowledgment. It does
not prefetch the next batch. Do not collect all reports if you need bounded memory.

Each BatchResult identifies a zero-based input `offset`, the submitted `count`,
and the server's `inserted` count (using native casing). An acknowledged range
does not mean every row was newly inserted. Inspect `inserted` and any application
constraints. These helpers do not provide a transaction across batches.

## Recover from a failed batch

`BatchWriteError` identifies the unconfirmed range with offset, count, outcome and the
original cause. Earlier yielded acknowledgments remain valid.

| Outcome | Meaning | Recovery |
| --- | --- | --- |
| `not_sent` | This range failed before its HTTP request | Fix the input or producer, then resume from a durable input checkpoint. |
| `unknown` | A request was attempted, but no valid success acknowledgment arrived | Reconcile the affected IDs or use application-defined idempotency before retrying. |

A partial server batch failure is retained as the underlying cause. No failed
batch is automatically replayed, including 401, 429, 503, lost responses, or
invalid acknowledgments. Do not turn a generic transport retry into a write retry.
After interruption, the last acknowledged offset is a useful checkpoint but
does not prove that the following in-flight batch was not committed.

Cancellation can surface as the language's cancellation error rather than a
batch error. Invalid options can fail before an input range exists. A producer
failure may report zero buffered rows; a bad row may be included in the rejected
range's count. Retain the original input if you need reliable recovery.

## Read one page at a time

```go
for row, err := range db.Rows.Scan(ctx, schema, "id", oc.PageOptions{PageSize: 2}) {
    if err != nil { return err }
    fmt.Println(row["id"], row["name"])
}
```

`PageSize` defaults to 1,000 and allows 1–10,000 rows. The key must be a
**unique, immutable, non-null string column with UTF-8 binary ordering**. The
example uses its string primary key `id`. Numeric or composite keys, custom
collations and arbitrary SQL query pagination are not supported by this helper.
Choose a smaller page when rows are large: the SDK decodes one complete page;
this is not a streaming JSON parser or a byte-bounded response reader.

The SDK uses supported SQL keyset reads: ordered rows with a bound `key > after`
predicate and LIMIT. Identifiers accept one ASCII table name or `namespace.table`
and one ASCII column name. They are validated and quoted; continuation values
are parameters. The server response must contain no more than the requested
rows and strictly increasing string keys, or the iterator raises a protocol error.

`Pages` yields RowPage with rows and `NextCursor`. Save a cursor only after
processing every row in its page successfully, then pass it as `After` to a
new iterator. The continuation is exclusive. A null/nil/None cursor means the
last page is complete; persist a separate completion flag if needed. A full final
page may require one additional empty request to discover completion. An empty
table yields no pages. The complete example demonstrates resuming after `b`.

There is no server cursor or snapshot held between requests. Inserts, updates
and deletes during traversal can change the result. For a stable export, use an
application-controlled immutable dataset or an appropriate server transaction
through the SQL API. Do not treat these helpers as snapshot isolation.

## Cancellation and cleanup

Use Go 1.23 range-over-function iteration. Every item is `(value, error)`; check the error before using the value. Pass `context.Context` for cancellation, including active HTTP calls. Break the range to stop consumption. Use `errors.As` for BatchWriteError and `errors.Is` for a wrapped cancellation cause.

Stopping iteration starts no further page or batch. It does not undo prior work
or prove that an in-flight server write was canceled. The SDK cannot close
application-owned files or database cursors hidden inside a producer; manage
those resources explicitly in the producer's own scope.

See [API v1](api/v1/README.md) for credential providers and errors, and
[quality checks](quality.md) for the common contract and verification scope.
