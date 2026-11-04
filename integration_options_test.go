package originchaindb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRotatingCredentialsAndSafeObservation(t *testing.T) {
	var headers []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = append(headers, r.Header.Get("Authorization"))
		w.Header().Set("X-OC-Request-Id", "request-123")
		w.Header().Set("Content-Type", "application/json")
		if len(headers) == 1 {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"code":"token_expired"}`)
			return
		}
		fmt.Fprint(w, `{"kind":"select","rows":[{"value":7}]}`)
	}))
	defer server.Close()
	var events []RequestEvent
	calls := 0
	client, err := NewClient(Config{URL: server.URL, TokenProvider: func(context.Context) (string, error) {
		calls++
		return fmt.Sprintf("secret-%d", calls), nil
	}, OnRequest: func(event RequestEvent) { events = append(events, event) }})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.SQL.Run(context.Background(), "private query")
	var failure *Error
	if !errors.As(err, &failure) || failure.Kind != "authentication" || failure.Code != "token_expired" {
		t.Fatalf("wrong error: %v", err)
	}
	if calls != 1 || len(headers) != 1 {
		t.Fatal("authentication failure was replayed")
	}
	if _, err = client.SQL.Run(context.Background(), "private query"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(headers, []string{"Bearer secret-1", "Bearer secret-2"}) {
		t.Fatal("token was not resolved per request")
	}
	// Windows clock resolution can report zero for a completed loopback call.
	if len(events) != 2 || events[0].Status != 401 || events[0].ErrorKind != "authentication" || events[1].ErrorKind != "" || events[1].RequestID != "request-123" || events[1].Duration < 0 {
		t.Fatalf("incorrect events: %+v", events)
	}
	for _, secret := range []string{"secret-", "private query", server.URL} {
		if strings.Contains(fmt.Sprint(events), secret) {
			t.Fatal("sensitive event metadata")
		}
	}
}

func TestCredentialFailureDoesNotSend(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	for _, provider := range []TokenProvider{
		func(context.Context) (string, error) { return "bad\ntoken", nil },
		func(context.Context) (string, error) { return "", errors.New("secret-provider-detail") },
	} {
		client, err := NewClient(Config{URL: server.URL, TokenProvider: provider})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Usage(context.Background())
		client.Close()
		var failure *Error
		if !errors.As(err, &failure) || failure.Kind != "credential" || strings.Contains(err.Error(), "secret") {
			t.Fatalf("wrong error: %v", err)
		}
	}
	if calls != 0 {
		t.Fatal("invalid credentials reached server")
	}
}

func TestObserverPanicDoesNotChangeResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }))
	defer server.Close()
	client, err := NewClient(Config{URL: server.URL, Token: "token", OnRequest: func(RequestEvent) { panic("observer") }})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err = client.Usage(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProviderGetsDeadlineAndPreCanceledCallsDoNotResolve(t *testing.T) {
	calls := 0
	client, err := NewClient(Config{URL: "https://example.invalid", TokenProvider: func(ctx context.Context) (string, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Error("provider has no deadline")
		}
		return "", errors.New("unavailable")
	}}, WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Usage(ctx)
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("canceled request invoked provider")
	}
	_, _ = client.Usage(context.Background())
	if calls != 1 {
		t.Fatal("provider not invoked")
	}
}

func TestDecodeRowsPreservesPrecisionAndRejectsBadTypes(t *testing.T) {
	type Product struct {
		ID     int64  `json:"id"`
		Amount string `json:"amount"`
	}
	result := &SQLResult{Rows: []map[string]any{{"id": json.Number("9007199254740993"), "amount": "1.00000000000000001"}}}
	rows, err := DecodeRows[Product](result)
	if err != nil || rows[0].ID != 9007199254740993 || rows[0].Amount != "1.00000000000000001" {
		t.Fatalf("lost precision: %v", err)
	}
	if _, ok := result.Rows[0]["id"].(json.Number); !ok {
		t.Fatal("raw result mutated")
	}
	result.Rows = append(result.Rows, map[string]any{"id": "invalid"})
	rows, err = DecodeRows[Product](result)
	var decode *RowDecodeError
	if rows != nil || !errors.As(err, &decode) || decode.Row != 1 {
		t.Fatal("invalid row accepted or partial list returned")
	}
}

func ExampleDecodeRows() {
	type Product struct {
		Name string `json:"name"`
	}
	result := &SQLResult{Rows: []map[string]any{{"name": "Widget"}}}
	products, _ := DecodeRows[Product](result)
	fmt.Println(products[0].Name)
	// Output: Widget
}

func TestCredentialProviderDeadline(t *testing.T) {
	var events []RequestEvent
	client, err := NewClient(Config{
		URL: "https://example.invalid",
		TokenProvider: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
		OnRequest: func(event RequestEvent) { events = append(events, event) },
	}, WithTimeout(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.SQL.Run(context.Background(), "SELECT 1")
	var failure *Error
	if !errors.As(err, &failure) || failure.Kind != "timeout" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("provider deadline was not preserved: %v", err)
	}
	if len(events) != 1 || events[0].Status != 0 || events[0].ErrorKind != "timeout" {
		t.Fatalf("unexpected provider event: %+v", events)
	}
}
