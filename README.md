# OriginChainDB for Go

The official Go client for SQL, vector search, full-text search, and graph queries.

[![CI](https://github.com/originchaindb/originchaindb-go/actions/workflows/ci.yml/badge.svg)](https://github.com/originchaindb/originchaindb-go/actions/workflows/ci.yml)

## Get started

Requires **Go 1.23+**. This is the unpublished 1.0 release candidate. Build from
source and pin a reviewed commit; registry publication is currently on hold.

```sh
# In your application, pin a reviewed commit:
go get github.com/originchaindb/originchaindb-go@<commit>
```

Copy your database address and access token from Connect into `OC_URL` and
`OC_TOKEN`. Keep tokens in your server's environment or secret manager.

```go
package main

import (
    "context"
    "log"
    oc "github.com/originchaindb/originchaindb-go"
)

func main() {
    db, err := oc.FromEnv()
    if err != nil { log.Fatal(err) }
    defer db.Close()
    result, err := db.SQL.Run(context.Background(), "SELECT $1 AS value", 7)
    if err != nil { log.Fatal(err) }
    log.Println(result.Rows)
}
```

Reuse the client for your application's lifetime. Pass values as parameters;
do not interpolate application data into query text.

## One API family

Use `sql`, `schemas`, `rows`, `vector`, `fts`, and `graph` for the same database
concepts across every OriginChainDB SDK. Names use this language's normal casing.

- SQL results include returned rows, confirmed counts, metadata and batch results.
- Full-text search provides the same hit structure for ranked and unranked queries.
- Access tokens are sent as bearer credentials to the exact database address.
- Failed writes are not retried automatically.

[Using the client](docs/api.md) Â· [Behavior](docs/contract.md) Ã‚Â· [Contributing](CONTRIBUTING.md) Ã‚Â·
[Changes](CHANGELOG.md) Ã‚Â· [Security](SECURITY.md)

## Development

```sh
go test ./...
go vet ./...
python tests/contract/run.py -- go run ./tests/consumer
```

The HTTP contract runner requires Python 3.10+ for testing only. No database
credentials are needed for the offline suite. Native runtime and dependency
requirements above apply to applications.
