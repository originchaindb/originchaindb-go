# Using the client

Create `db` as shown in the [quickstart](../README.md). These independent examples
assume an existing `demo.items` schema and an appropriate search index.

```go
result, err := db.SQL.Run(ctx, "SELECT $1 AS value", 7)
if err != nil { return err }
fmt.Println(result.Rows)
vectors, err := db.Vector.Search(ctx, "demo.items", []float64{1, 0}, oc.VectorSearchOptions{})
if err != nil { return err }
fmt.Println(vectors)
text, err := db.FTS.Search(ctx, "demo.items", "body", "hello", oc.FTSSearchOptions{})
if err != nil { return err }
fmt.Println(text.Hits)
```

## Results, errors, and lifetime

The fragment belongs inside a function returning `error`; `ctx` is a
`context.Context` and `oc` imports this module. `SQLResult.Rows` contains maps;
JSON numbers are `json.Number`. `RowsAffected` is `*int64`; nil means unknown.
Vector hits expose `ID`, `Score`, `Metadata`, and `Raw`. Full-text results expose
`Hits`, `Facets`, and `Raw`.

Use `errors.As(err, &failure)` with `var failure *oc.Error`; inspect `Status`,
`RequestID`, and `Partial`. `Kind` distinguishes protocol, transport, and batch errors.

Configure `oc.FromEnv(oc.WithTimeout(10 * time.Second))`. Pass the application's
context to every operation; its deadline and cancellation propagate to HTTP.
Call `db.Close()` when the application shuts down.

See the [shared behavior contract](contract.md) for parameter binding, batch
semantics, defaults, and retry behavior. The [contract consumer](../tests/consumer/main.go)
exercises the full common API against a local fixture.
