// Package originchaindb connects Go applications to OriginChainDB over HTTPS.
// Create one Client per database and reuse it across goroutines. Every network
// operation takes a context. Requests are sent once unless Config.MaxRetries
// allows a vector index read to be sent again; writes are never replayed.
package originchaindb

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const Version = "1.0.0-rc.1"
const maxResponseBytes int64 = 32 << 20

// Client is safe for concurrent use. Close releases its idle connections.
type Client struct {
	endpoint string
	token    string
	provider TokenProvider
	observer func(RequestEvent)
	http     *http.Client
	// retries and retryTime apply to vector index reads only.
	retries       int
	retryTime     time.Duration
	SQL           *SQLService
	Rows          *RowsService
	Schemas       *SchemasService
	Vector        *VectorService
	FTS           *FTSService
	Graph         *GraphService
	VectorIndexes *VectorIndexesService
}

// Option configures a client before its first request.
type Option func(*http.Client) error

// WithTimeout sets the total time available to an individual HTTP request.
func WithTimeout(timeout time.Duration) Option {
	return func(c *http.Client) error {
		if timeout <= 0 {
			return errors.New("timeout must be positive")
		}
		c.Timeout = timeout
		return nil
	}
}

// WithTransport uses an application-owned transport, for custom TLS or testing.
// Redirects remain disabled. Client.Close closes idle connections if supported.
func WithTransport(transport http.RoundTripper) Option {
	return func(c *http.Client) error {
		if transport == nil {
			return errors.New("transport must not be nil")
		}
		c.Transport = transport
		return nil
	}
}

// New uses the address copied from Connect. Its path is opaque: no database ID
// is inferred. HTTPS is required except for a loopback development server.
func New(address, token string, options ...Option) (*Client, error) {
	return NewClient(Config{URL: address, Token: token}, options...)
}

// NewClient adds rotating credentials and local observation to New. Configure a
// client once, then share it. Provider and observer callbacks must be concurrent-safe.
func NewClient(config Config, options ...Option) (*Client, error) {
	address, token := config.URL, config.Token
	parsed, err := url.Parse(address)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil ||
		strings.ContainsAny(address, "?#\\") || strings.IndexFunc(address, func(r rune) bool { return r <= 32 || r == 127 }) >= 0 {
		return nil, errors.New("invalid database URL; copy the address from Connect")
	}
	loopback := parsed.Hostname() == "localhost"
	if ip := net.ParseIP(parsed.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
		return nil, errors.New("database URL must use HTTPS; HTTP is allowed only on loopback")
	}
	if config.TokenProvider != nil && token != "" {
		return nil, errors.New("configure either Token or TokenProvider, not both")
	}
	if config.TokenProvider == nil && !validToken(token) {
		return nil, errors.New("token must be a nonempty printable ASCII value without spaces")
	}
	if config.MaxRetries < 0 || config.MaxRetries > 99 {
		return nil, errors.New("MaxRetries must be from 0 to 99")
	}
	if config.MaxRetryTime < 0 {
		return nil, errors.New("MaxRetryTime must not be negative")
	}
	retryTime := config.MaxRetryTime
	if retryTime == 0 {
		retryTime = defaultMaxRetryTime
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 20
	transport.MaxConnsPerHost = 100
	client := &http.Client{Timeout: 60 * time.Second, Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	for _, option := range options {
		if err := option(client); err != nil {
			return nil, err
		}
	}
	c := &Client{endpoint: strings.TrimRight(address, "/"), token: token, http: client, provider: config.TokenProvider, observer: config.OnRequest,
		retries: config.MaxRetries, retryTime: retryTime}
	c.SQL = &SQLService{c}
	c.Rows = &RowsService{c}
	c.Schemas = &SchemasService{c}
	c.Vector = &VectorService{c}
	c.FTS = &FTSService{c}
	c.Graph = &GraphService{c}
	c.VectorIndexes = &VectorIndexesService{c}
	return c, nil
}

// FromEnv reads only OC_URL and OC_TOKEN.
func FromEnv(options ...Option) (*Client, error) {
	return New(os.Getenv("OC_URL"), os.Getenv("OC_TOKEN"), options...)
}

// Close releases idle HTTP connections; it does not interrupt active calls.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// String deliberately omits the access token and database URL.
func (c *Client) String() string   { return "OriginChainDB Client" }
func (c *Client) GoString() string { return "OriginChainDB Client" }

// Error is an HTTP, transport, timeout, protocol, or batch error. Error() never
// includes response data or credentials. Body and Partial are opt-in diagnostics.
//
// Code, Message, Reason, Details and Retryable are read from the server's error
// envelope, {"error": {"code", "message", "retryable", "details"}}, when it sends
// one. Branch on Code plus Reason (error.details.reason), never on Status alone.
type Error struct {
	Kind       string
	Status     int
	Code       string
	SQLState   string
	RequestID  string
	RetryAfter string
	Body       any
	Partial    *SQLResult
	// Message is the envelope's error.message (else its code), or the text of a
	// flat {"error": "..."} body. It is server text: Error() leaves it out.
	Message string
	// Reason is the envelope's error.details.reason, for example
	// "exact_index_capacity".
	Reason string
	// Details is the envelope's error.details object.
	Details map[string]any
	// Retryable is the envelope's error.retryable: true only when the server
	// said the identical request can succeed later without changes.
	Retryable bool
	cause     error
}

// ErrRecordAPINotEnabled matches, with errors.Is, the HTTP 501 that a database
// answers to every VectorIndexes call when the vector index record operations
// are not enabled for it. Sending the request again does not help. The Vector
// service's per-table calls are not affected.
var ErrRecordAPINotEnabled = errors.New("vector index record operations are not enabled for this database")

func (e *Error) Error() string {
	return fmt.Sprintf("OriginChainDB %s error (HTTP %d)", e.Kind, e.Status)
}
func (e *Error) Unwrap() error { return e.cause }

// Is reports whether target is ErrRecordAPINotEnabled and e is that 501.
func (e *Error) Is(target error) bool {
	return target == ErrRecordAPINotEnabled && e.Status == http.StatusNotImplemented && e.Reason == "record_api_not_enabled"
}

func (c *Client) request(ctx context.Context, method, path string, query url.Values, body any, textBody *string) (any, error) {
	value, _, err := c.send(ctx, exchange{method: method, path: path, query: query, body: func() ([]byte, string, error) {
		if textBody != nil {
			return []byte(*textBody), "text/plain", nil
		}
		if body != nil {
			data, err := json.Marshal(body)
			if err != nil {
				return nil, "", fmt.Errorf("encode request: %w", err)
			}
			return data, "application/json", nil
		}
		return nil, "", nil
	}})
	return value, err
}

// exchange is one logical request. body returns the request body and its
// content type; a nil body, or one that returns nil data, sends none. retries
// is how many more times a failure that is safe to repeat may be sent again;
// zero sends the request once.
type exchange struct {
	method  string
	path    string
	query   url.Values
	body    func() ([]byte, string, error)
	retries int
}

// send performs one logical request and returns the decoded response and its
// raw bytes. The credential, the logical request ID and the observer event
// belong to the logical request: every attempt shares them.
func (c *Client) send(parent context.Context, ex exchange) (value any, raw []byte, resultErr error) {
	event := RequestEvent{Method: ex.method, Operation: operation(ex.path)}
	started := time.Now()
	defer func() {
		event.Duration = time.Since(started)
		if resultErr != nil {
			event.ErrorKind = "client"
			var sdkError *Error
			if errors.As(resultErr, &sdkError) {
				event.ErrorKind = sdkError.Kind
			}
		}
		notify(c.observer, event)
	}()
	// The first attempt's deadline includes credential resolution. A retry
	// gets a fresh deadline of its own.
	ctx, cancel := context.WithTimeout(parent, c.http.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, nil, contextError(err)
	}
	token := c.token
	if c.provider != nil {
		var err error
		token, err = c.provider(ctx)
		if err != nil {
			kind := "credential"
			if errors.Is(err, context.DeadlineExceeded) {
				kind = "timeout"
			} else if errors.Is(err, context.Canceled) {
				kind = "canceled"
			}
			return nil, nil, &Error{Kind: kind, cause: err}
		}
		if !validToken(token) {
			return nil, nil, &Error{Kind: "credential"}
		}
	}
	var data []byte
	contentType := ""
	if ex.body != nil {
		var err error
		if data, contentType, err = ex.body(); err != nil {
			return nil, nil, err
		}
	}
	address := c.endpoint + "/" + ex.path
	if len(ex.query) > 0 {
		address += "?" + ex.query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, ex.method, address, bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "originchaindb-go/"+Version)
	if data != nil {
		request.Header.Set("Content-Type", contentType)
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return nil, nil, err
	}
	request.Header.Set("X-OC-Logical-Request-Id", hex.EncodeToString(id))
	retryDeadline := started.Add(c.retryTime)
	for attempt := 0; ; attempt++ {
		// The observer reports the last attempt.
		event.Status, event.RequestID = 0, ""
		var response *http.Response
		if attempt == 0 {
			response, raw, err = c.roundTrip(request)
		} else {
			// A retry is the same request, headers and body included, with a
			// deadline of its own. The answer is read before it is released.
			retryCtx, retryCancel := context.WithTimeout(parent, c.http.Timeout)
			retry := request.Clone(retryCtx)
			if retry.Body, err = request.GetBody(); err == nil {
				response, raw, err = c.roundTrip(retry)
			}
			retryCancel()
		}
		if response == nil {
			kind := "transport"
			if errors.Is(err, context.DeadlineExceeded) {
				kind = "timeout"
			}
			if errors.Is(err, context.Canceled) {
				kind = "canceled"
			}
			failure := &Error{Kind: kind, cause: err}
			if attempt < ex.retries && kind != "canceled" && parent.Err() == nil {
				again, waitErr := retryPause(parent, retryDeadline, retryBackoff(attempt))
				if waitErr != nil {
					return nil, nil, waitErr
				}
				if again {
					continue
				}
			}
			return nil, nil, failure
		}
		event.Status = response.StatusCode
		event.RequestID = response.Header.Get("X-OC-Request-Id")
		if err != nil {
			return nil, nil, &Error{Kind: "transport", Status: response.StatusCode, cause: err}
		}
		if int64(len(raw)) > maxResponseBytes {
			return nil, nil, &Error{Kind: "protocol", Status: response.StatusCode}
		}
		value = nil
		var decodeErr error
		if len(raw) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			decodeErr = decoder.Decode(&value)
			if decodeErr == nil {
				var extra any
				if decoder.Decode(&extra) != io.EOF {
					decodeErr = errors.New("trailing JSON content")
				}
			}
			if decodeErr != nil {
				value = string(raw)
			}
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			failure := httpError(response, value)
			if attempt < ex.retries && retryableStatus(response.StatusCode) {
				wait, ok := parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
				if !ok {
					wait = retryBackoff(attempt)
				}
				again, waitErr := retryPause(parent, retryDeadline, wait)
				if waitErr != nil {
					return nil, nil, waitErr
				}
				if again {
					continue
				}
			}
			return nil, nil, failure
		}
		if decodeErr != nil && !strings.HasPrefix(response.Header.Get("Content-Type"), "text/") {
			return nil, nil, &Error{Kind: "protocol", Status: response.StatusCode, RequestID: response.Header.Get("X-OC-Request-Id")}
		}
		return value, raw, nil
	}
}

// roundTrip sends one attempt and reads its whole answer. A nil response
// means no answer arrived.
func (c *Client) roundTrip(request *http.Request) (*http.Response, []byte, error) {
	response, err := c.http.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	return response, raw, err
}

func contextError(err error) *Error {
	kind := "canceled"
	if errors.Is(err, context.DeadlineExceeded) {
		kind = "timeout"
	}
	return &Error{Kind: kind, cause: err}
}

func httpError(response *http.Response, value any) *Error {
	e := &Error{Kind: "http", Status: response.StatusCode, RequestID: response.Header.Get("X-OC-Request-Id"), RetryAfter: response.Header.Get("Retry-After"), Body: value}
	if response.StatusCode == 401 {
		e.Kind = "authentication"
	}
	if response.StatusCode == 403 {
		e.Kind = "permission"
	}
	if response.StatusCode == 429 {
		e.Kind = "rate_limit"
	}
	if object, ok := value.(map[string]any); ok {
		e.SQLState, _ = object["sqlstate"].(string)
		e.Code, _ = object["code"].(string)
		switch nested := object["error"].(type) {
		case map[string]any:
			e.Code, _ = nested["code"].(string)
			e.Message, _ = nested["message"].(string)
			if e.Message == "" {
				e.Message = e.Code
			}
			e.Details, _ = nested["details"].(map[string]any)
			e.Reason, _ = e.Details["reason"].(string)
			e.Retryable, _ = nested["retryable"].(bool)
		case string:
			e.Message = nested
		}
	}
	return e
}

func segment(value string) (string, error) {
	if value == "" || value == "." || value == ".." || strings.IndexFunc(value, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		return "", errors.New("path name must be nonempty and contain no control characters")
	}
	return url.PathEscape(value), nil
}
func path(parts ...string) (string, error) {
	for i, p := range parts {
		escaped, err := segment(p)
		if err != nil {
			return "", err
		}
		parts[i] = escaped
	}
	return strings.Join(parts, "/"), nil
}

// Usage returns the database usage envelope without changing server fields.
func (c *Client) Usage(ctx context.Context) (any, error) {
	return c.request(ctx, http.MethodGet, "usage", nil, nil, nil)
}

// QueryMetrics returns the server's process-level query metrics.
func (c *Client) QueryMetrics(ctx context.Context, minutes, bucketSeconds int) (any, error) {
	return c.request(ctx, http.MethodGet, "query-metrics", url.Values{"minutes": {fmt.Sprint(minutes)}, "bucket_seconds": {fmt.Sprint(bucketSeconds)}}, nil, nil)
}
