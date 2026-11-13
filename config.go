package originchaindb

import (
	"context"
	"strings"
	"time"
)

// TokenProvider resolves the current bearer token once per request. It must honor
// cancellation. A failed request is never replayed after refreshing credentials.
type TokenProvider func(context.Context) (string, error)

// Config holds optional application integration settings. Supply exactly one of
// Token and TokenProvider. Callbacks are local; the SDK exports no telemetry.
type Config struct {
	URL           string
	Token         string
	TokenProvider TokenProvider
	OnRequest     func(RequestEvent)
	// MaxRetries is how many more times a VectorIndexes read (describe, list,
	// fetch, count, statistics, search, job status) is sent after a transport
	// failure, a timeout, or HTTP 429, 500, 502, 503 or 504. Every attempt
	// keeps the logical request ID. Zero, the default, sends each request once.
	// It goes from 0 to 99. Writes, and the other services' operations, are
	// always sent once.
	MaxRetries int
	// MaxRetryTime bounds the time from the start of a call within which a
	// retry may begin; a wait that would end later is not taken. A Retry-After
	// header sets the wait when present, else a jittered backoff up to four
	// seconds does. Zero selects 30 seconds.
	MaxRetryTime time.Duration
}

func (Config) String() string   { return "OriginChainDB Config" }
func (Config) GoString() string { return "OriginChainDB Config" }

// RequestEvent contains only transport metadata, never URLs, schema names,
// parameters, SQL text, request/response bodies, headers or credentials.
// Status is zero if no response headers arrived. An empty ErrorKind means the
// HTTP exchange succeeded; result decoding can still fail after this event.
type RequestEvent struct {
	Method    string
	Operation string
	Duration  time.Duration
	Status    int
	RequestID string
	ErrorKind string
}

func validToken(token string) bool {
	return token != "" && strings.IndexFunc(token, func(r rune) bool { return r <= 32 || r >= 127 }) < 0
}

func operation(path string) string {
	first := strings.SplitN(path, "/", 2)[0]
	switch first {
	case "sql", "rows", "schemas", "vector", "fts", "graph", "cypher", "usage", "query-metrics":
		return first
	default:
		return "other"
	}
}

func notify(observer func(RequestEvent), event RequestEvent) {
	if observer == nil {
		return
	}
	defer func() { _ = recover() }()
	observer(event)
}
