// Package originchaindb connects Go applications to OriginChainDB over HTTPS.
// Create one Client per database and reuse it across goroutines. Every network
// operation takes a context. Requests are never replayed automatically.
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
	http     *http.Client
	SQL      *SQLService
	Rows     *RowsService
	Schemas  *SchemasService
	Vector   *VectorService
	FTS      *FTSService
	Graph    *GraphService
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
	if token == "" || strings.IndexFunc(token, func(r rune) bool { return r <= 32 || r >= 127 }) >= 0 {
		return nil, errors.New("token must be a nonempty printable ASCII value without spaces")
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
	c := &Client{endpoint: strings.TrimRight(address, "/"), token: token, http: client}
	c.SQL = &SQLService{c}
	c.Rows = &RowsService{c}
	c.Schemas = &SchemasService{c}
	c.Vector = &VectorService{c}
	c.FTS = &FTSService{c}
	c.Graph = &GraphService{c}
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
type Error struct {
	Kind       string
	Status     int
	Code       string
	SQLState   string
	RequestID  string
	RetryAfter string
	Body       any
	Partial    *SQLResult
	cause      error
}

func (e *Error) Error() string {
	return fmt.Sprintf("OriginChainDB %s error (HTTP %d)", e.Kind, e.Status)
}
func (e *Error) Unwrap() error { return e.cause }

func (c *Client) request(ctx context.Context, method, path string, query url.Values, body any, textBody *string) (any, error) {
	var data []byte
	var err error
	contentType := "application/json"
	if textBody != nil {
		data = []byte(*textBody)
		contentType = "text/plain"
	} else if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
	}
	address := c.endpoint + "/" + path
	if len(query) > 0 {
		address += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "originchaindb-go/"+Version)
	if data != nil {
		request.Header.Set("Content-Type", contentType)
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return nil, err
	}
	request.Header.Set("X-OC-Logical-Request-Id", hex.EncodeToString(id))
	response, err := c.http.Do(request)
	if err != nil {
		kind := "transport"
		if errors.Is(err, context.DeadlineExceeded) {
			kind = "timeout"
		}
		if errors.Is(err, context.Canceled) {
			kind = "canceled"
		}
		return nil, &Error{Kind: kind, cause: err}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, &Error{Kind: "transport", Status: response.StatusCode, cause: err}
	}
	if int64(len(raw)) > maxResponseBytes {
		return nil, &Error{Kind: "protocol", Status: response.StatusCode}
	}
	var value any
	if len(raw) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		err = decoder.Decode(&value)
		if err == nil {
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				err = errors.New("trailing JSON content")
			}
		}
		if err != nil {
			value = string(raw)
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
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
			if nested, ok := object["error"].(map[string]any); ok {
				e.Code, _ = nested["code"].(string)
			}
		}
		return nil, e
	}
	if err != nil && !strings.HasPrefix(response.Header.Get("Content-Type"), "text/") {
		return nil, &Error{Kind: "protocol", Status: response.StatusCode, RequestID: response.Header.Get("X-OC-Request-Id")}
	}
	return value, nil
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
