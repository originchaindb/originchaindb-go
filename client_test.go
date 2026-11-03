package originchaindb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConfigurationRejectsUnsafeInputs(t *testing.T) {
	for _, address := range []string{"", "http://example.com", "https://user:pass@example.com", "https://example.com?a=1", "https://example.com#fragment", "https://example.com/\n"} {
		if _, err := New(address, "test"); err == nil {
			t.Errorf("accepted invalid URL %q", address)
		}
	}
	for _, token := range []string{"", "two words", "line\nbreak", "雪"} {
		if _, err := New("https://example.com", token); err == nil {
			t.Error("accepted invalid token")
		}
	}
	c, err := New("https://example.com/path", "credential-not-for-logs")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, repr := range []string{fmt.Sprint(c), fmt.Sprintf("%#v", c)} {
		if strings.Contains(repr, "credential-not-for-logs") {
			t.Fatal("client formatting exposed credential")
		}
	}
}

func TestCancellationAndTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}))
	defer server.Close()
	c, err := New(server.URL, "test", WithTimeout(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.SQL.Run(context.Background(), "SELECT 1")
	var failure *Error
	if !errors.As(err, &failure) || failure.Kind != "timeout" {
		t.Fatalf("expected timeout, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.SQL.Run(ctx, "SELECT 1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation cause lost: %v", err)
	}
}

func TestFullTextShapes(t *testing.T) {
	for _, payload := range []string{`["a"]`, `[{"doc_id":"a","score":1.5}]`, `{"hits":[{"doc_id":"a","score":1.5,"highlights":{"body":["<em>a</em>"]}}],"facets":{"tag":[]}}`} {
		decoder := json.NewDecoder(strings.NewReader(payload))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		result, err := decodeFTS(value)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Hits) != 1 || result.Hits[0].DocID != "a" {
			t.Fatal("normalized hit missing")
		}
	}
	for _, value := range []any{nil, map[string]any{}, []any{123}, []any{map[string]any{"doc_id": "a", "score": "invalid"}}} {
		if _, err := decodeFTS(value); err == nil {
			t.Fatal("accepted invalid search response")
		}
	}
}
