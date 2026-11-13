package originchaindb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Every test routes the client to a local httptest engine: no live database.
// Each request's exact method, raw path, headers and body bytes are checked.

const testPrefix = "/opaque/db%2Fone"

var logicalID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type answer struct {
	status  int
	body    string
	headers map[string]string
}

func ok(body string) answer { return answer{status: 200, body: body} }

func envelope(status int, code string, retryable bool, details string) answer {
	body := fmt.Sprintf(`{"error":{"code":%q,"message":"message for %s","status":%d,"retryable":%t`, code, code, status, retryable)
	if details != "" {
		body += `,"details":` + details
	}
	return answer{status: status, body: body + "}}"}
}

type seen struct {
	method string
	target string
	header http.Header
	body   string
}

// engine answers from a queue (the last answer repeats), or from handle, and
// records every request.
type engine struct {
	t       *testing.T
	mu      sync.Mutex
	answers []answer
	handle  func(n int, call seen, w http.ResponseWriter) bool
	calls   []seen
	server  *httptest.Server
}

func newEngine(t *testing.T, answers ...answer) *engine {
	e := &engine{t: t, answers: answers}
	e.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		call := seen{r.Method, r.RequestURI, r.Header.Clone(), string(body)}
		e.mu.Lock()
		e.calls = append(e.calls, call)
		n := len(e.calls)
		handle := e.handle
		e.mu.Unlock()
		if handle != nil && handle(n, call, w) {
			return
		}
		a := e.answers[min(n, len(e.answers))-1]
		for key, value := range a.headers {
			w.Header().Set(key, value)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.status)
		_, _ = io.WriteString(w, a.body)
	}))
	t.Cleanup(e.server.Close)
	return e
}

func (e *engine) client(config Config) *Client {
	e.t.Helper()
	config.URL = e.server.URL + testPrefix
	if config.TokenProvider == nil {
		config.Token = "test-bearer"
	}
	c, err := NewClient(config)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(c.Close)
	return c
}

func (e *engine) setHandle(handle func(n int, call seen, w http.ResponseWriter) bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handle = handle
}

func (e *engine) vi() *VectorIndexesService { return e.client(Config{}).VectorIndexes }

func (e *engine) seen() []seen {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]seen(nil), e.calls...)
}

// none marks a request that must carry no body.
const none = "\x00no body"

func assertCall(t *testing.T, call seen, method, path, body string) {
	t.Helper()
	if call.method != method {
		t.Errorf("method %s, want %s", call.method, method)
	}
	if want := testPrefix + "/" + path; call.target != want {
		t.Errorf("path %s, want %s", call.target, want)
	}
	if body == none {
		if call.body != "" || call.header.Get("Content-Type") != "" {
			t.Errorf("request carried a body %q (%s)", call.body, call.header.Get("Content-Type"))
		}
	} else {
		if call.body != body {
			t.Errorf("body\n got %s\nwant %s", call.body, body)
		}
		if got := call.header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type %q", got)
		}
	}
	if got := call.header.Get("Authorization"); got != "Bearer test-bearer" {
		t.Errorf("authorization %q", got)
	}
	if got := call.header.Get("Accept"); got != "application/json" {
		t.Errorf("accept %q", got)
	}
	if got := call.header.Get("User-Agent"); got != "originchaindb-go/"+Version {
		t.Errorf("user agent %q", got)
	}
	if !logicalID.MatchString(call.header.Get("X-OC-Logical-Request-Id")) {
		t.Errorf("logical request id %q", call.header.Get("X-OC-Logical-Request-Id"))
	}
}

func onlyCall(t *testing.T, e *engine) seen {
	t.Helper()
	calls := e.seen()
	if len(calls) != 1 {
		t.Fatalf("%d requests, want 1", len(calls))
	}
	return calls[0]
}

type requestCase struct {
	name   string
	call   func(context.Context, *VectorIndexesService) (map[string]any, error)
	method string
	path   string
	body   string
}

func requestCases() []requestCase {
	type vi = VectorIndexesService
	type m = map[string]any
	capacity := int64(500)
	return []requestCase{
		{"create index", func(ctx context.Context, s *vi) (m, error) {
			return s.CreateIndex(ctx, "movies", CreateIndexOptions{Dimension: 3, Metric: "cosine", Sparse: true,
				TextFields:       []VectorTextField{{Name: "body", Analyzer: "standard_en_v1"}},
				VisibilityFields: []string{"tenant"}, IdentifierProfile: "ascii", DeletionProtection: "enabled",
				Tags: map[string]string{"team": "search"}})
		}, "POST", "vector-indexes",
			`{"name":"movies","dimension":3,"metric":"cosine","sparse":true,"text_fields":[{"name":"body","analyzer":"standard_en_v1"}],"visibility_fields":["tenant"],"identifier_profile":"ascii","deletion_protection":"enabled","tags":{"team":"search"}}`},
		{"create index minimal", func(ctx context.Context, s *vi) (m, error) {
			return s.CreateIndex(ctx, "movies", CreateIndexOptions{Dimension: 3})
		}, "POST", "vector-indexes", `{"name":"movies","dimension":3}`},
		{"describe index escapes the name", func(ctx context.Context, s *vi) (m, error) {
			return s.DescribeIndex(ctx, "movies/one")
		}, "GET", "vector-indexes/movies%2Fone", none},
		{"list indexes", func(ctx context.Context, s *vi) (m, error) {
			return s.ListIndexes(ctx, ListIndexesOptions{Prefix: "mo", Limit: 10, Cursor: "c1"})
		}, "POST", "vector-indexes/list", `{"prefix":"mo","limit":10,"cursor":"c1"}`},
		{"list indexes by name", func(ctx context.Context, s *vi) (m, error) {
			return s.ListIndexes(ctx, ListIndexesOptions{Name: "movies"})
		}, "POST", "vector-indexes/list", `{"name":"movies"}`},
		{"list indexes without a filter sends an empty object", func(ctx context.Context, s *vi) (m, error) {
			return s.ListIndexes(ctx, ListIndexesOptions{})
		}, "POST", "vector-indexes/list", `{}`},
		{"configure index", func(ctx context.Context, s *vi) (m, error) {
			return s.ConfigureIndex(ctx, "movies", ConfigureIndexOptions{DeletionProtection: "enabled", Tags: map[string]string{"old": ""}})
		}, "PATCH", "vector-indexes/movies", `{"deletion_protection":"enabled","tags":{"old":""}}`},
		{"delete index", func(ctx context.Context, s *vi) (m, error) {
			return s.DeleteIndex(ctx, "movies")
		}, "DELETE", "vector-indexes/movies", none},
		{"index statistics", func(ctx context.Context, s *vi) (m, error) {
			return s.IndexStats(ctx, "movies")
		}, "GET", "vector-indexes/movies/stats", none},
		{"create namespace", func(ctx context.Context, s *vi) (m, error) {
			return s.CreateNamespace(ctx, "movies", "ns1", CreateNamespaceOptions{DeclaredCapacity: &capacity})
		}, "POST", "vector-indexes/movies/namespaces/create", `{"name":"ns1","declared_capacity":500}`},
		{"create namespace minimal", func(ctx context.Context, s *vi) (m, error) {
			return s.CreateNamespace(ctx, "movies", "ns1", CreateNamespaceOptions{})
		}, "POST", "vector-indexes/movies/namespaces/create", `{"name":"ns1"}`},
		{"describe namespace", func(ctx context.Context, s *vi) (m, error) {
			return s.DescribeNamespace(ctx, "movies", "ns1")
		}, "POST", "vector-indexes/movies/namespaces/describe", `{"name":"ns1"}`},
		{"list namespaces", func(ctx context.Context, s *vi) (m, error) {
			return s.ListNamespaces(ctx, "movies", ListNamespacesOptions{Prefix: "n", Limit: 2, Cursor: "n1"})
		}, "POST", "vector-indexes/movies/namespaces/list", `{"prefix":"n","limit":2,"cursor":"n1"}`},
		{"configure namespace sets the capacity", func(ctx context.Context, s *vi) (m, error) {
			return s.ConfigureNamespace(ctx, "movies", "ns1", Ptr(int64(100)))
		}, "POST", "vector-indexes/movies/namespaces/configure", `{"name":"ns1","declared_capacity":100}`},
		{"configure namespace removes the capacity", func(ctx context.Context, s *vi) (m, error) {
			return s.ConfigureNamespace(ctx, "movies", "ns1", nil)
		}, "POST", "vector-indexes/movies/namespaces/configure", `{"name":"ns1","declared_capacity":null}`},
		{"delete namespace sends the confirmation", func(ctx context.Context, s *vi) (m, error) {
			return s.DeleteNamespace(ctx, "movies", "ns1")
		}, "POST", "vector-indexes/movies/namespaces/delete", `{"name":"ns1","confirm":"ns1"}`},
		{"build index", func(ctx context.Context, s *vi) (m, error) {
			return s.BuildIndex(ctx, "movies", "ns1", BuildIndexOptions{Seed: Ptr(int64(7))})
		}, "POST", "vector-indexes/movies/namespaces/build-index", `{"namespace":"ns1","seed":7}`},
		{"fetch", func(ctx context.Context, s *vi) (m, error) {
			return s.Fetch(ctx, "movies", "ns1", []string{"a", "b"}, FetchOptions{IncludeValues: Ptr(true), MetadataFields: []string{"genre"}, TextFields: []string{"body"}})
		}, "POST", "vector-indexes/movies/records/fetch", `{"namespace":"ns1","ids":["a","b"],"include_values":true,"metadata_fields":["genre"],"text_fields":["body"]}`},
		{"fetch can turn metadata off", func(ctx context.Context, s *vi) (m, error) {
			return s.Fetch(ctx, "movies", "ns1", []string{"a"}, FetchOptions{IncludeSparse: Ptr(true), IncludeMetadata: Ptr(false)})
		}, "POST", "vector-indexes/movies/records/fetch", `{"namespace":"ns1","ids":["a"],"include_sparse":true,"include_metadata":false}`},
		{"fetch without ids sends an empty list", func(ctx context.Context, s *vi) (m, error) {
			return s.Fetch(ctx, "movies", "ns1", nil, FetchOptions{})
		}, "POST", "vector-indexes/movies/records/fetch", `{"namespace":"ns1","ids":[]}`},
		{"fetch by filter expression", func(ctx context.Context, s *vi) (m, error) {
			return s.FetchByFilter(ctx, "movies", "ns1", FetchByFilterOptions{FilterExpr: map[string]any{"genre": map[string]any{"$eq": "drama"}}, Limit: 1, Cursor: "f1", IncludeValues: Ptr(true)})
		}, "POST", "vector-indexes/movies/records/fetch-by-filter", `{"namespace":"ns1","filter_expr":{"genre":{"$eq":"drama"}},"limit":1,"cursor":"f1","include_values":true}`},
		{"fetch by equality filter", func(ctx context.Context, s *vi) (m, error) {
			return s.FetchByFilter(ctx, "movies", "ns1", FetchByFilterOptions{Filter: map[string]any{"genre": "drama"}})
		}, "POST", "vector-indexes/movies/records/fetch-by-filter", `{"namespace":"ns1","filter":{"genre":"drama"}}`},
		{"list ids", func(ctx context.Context, s *vi) (m, error) {
			return s.ListIDs(ctx, "movies", "ns1", ListIDsOptions{Prefix: "doc-", Limit: 2, Cursor: "i1"})
		}, "POST", "vector-indexes/movies/records/list-ids", `{"namespace":"ns1","prefix":"doc-","limit":2,"cursor":"i1"}`},
		{"count with a filter, unescaped", func(ctx context.Context, s *vi) (m, error) {
			return s.Count(ctx, "movies", "ns1", CountOptions{Filter: map[string]any{"title": "<b> & é"}})
		}, "POST", "vector-indexes/movies/records/count", `{"namespace":"ns1","filter":{"title":"<b> & é"}}`},
		{"count everything", func(ctx context.Context, s *vi) (m, error) {
			return s.Count(ctx, "movies", "ns1", CountOptions{})
		}, "POST", "vector-indexes/movies/records/count", `{"namespace":"ns1"}`},
		{"update by id", func(ctx context.Context, s *vi) (m, error) {
			return s.Update(ctx, "movies", "ns1", "a", UpdateOptions{Values: []float64{0.5, 1},
				SparseValues: map[string]any{"indices": []int{1}, "values": []float64{0.5}}, MetadataMerge: map[string]any{"year": 1999},
				MetadataRemove: []string{"old"}, TextSet: map[string]string{"body": "new"}, TextRemove: []string{"title"}, IfRevisionToken: "rev-1"})
		}, "POST", "vector-indexes/movies/records/update",
			`{"namespace":"ns1","id":"a","values":[0.5,1],"sparse_values":{"indices":[1],"values":[0.5]},"metadata_merge":{"year":1999},"metadata_remove":["old"],"text_set":{"body":"new"},"text_remove":["title"],"if_revision_token":"rev-1"}`},
		{"update replaces metadata", func(ctx context.Context, s *vi) (m, error) {
			return s.Update(ctx, "movies", "ns1", "a", UpdateOptions{MetadataReplace: map[string]any{"k": "v"}})
		}, "POST", "vector-indexes/movies/records/update", `{"namespace":"ns1","id":"a","metadata_replace":{"k":"v"}}`},
		{"update by filter, dry run", func(ctx context.Context, s *vi) (m, error) {
			return s.UpdateByFilter(ctx, "movies", "ns1", UpdateByFilterOptions{FilterExpr: map[string]any{"year": map[string]any{"$lt": 2000}},
				MetadataMerge: map[string]any{"old": true}, MetadataRemove: []string{"x"}, DryRun: true})
		}, "POST", "vector-indexes/movies/records/update", `{"namespace":"ns1","filter_expr":{"year":{"$lt":2000}},"metadata_merge":{"old":true},"metadata_remove":["x"],"dry_run":true}`},
		{"update by filter", func(ctx context.Context, s *vi) (m, error) {
			return s.UpdateByFilter(ctx, "movies", "ns1", UpdateByFilterOptions{Filter: map[string]any{"genre": "a"}, MetadataRemove: []string{"x"}})
		}, "POST", "vector-indexes/movies/records/update", `{"namespace":"ns1","filter":{"genre":"a"},"metadata_remove":["x"]}`},
		{"delete by ids", func(ctx context.Context, s *vi) (m, error) {
			return s.Delete(ctx, "movies", "ns1", []string{"a", "b"})
		}, "POST", "vector-indexes/movies/records/delete", `{"namespace":"ns1","ids":["a","b"]}`},
		{"delete by filter", func(ctx context.Context, s *vi) (m, error) {
			return s.DeleteByFilter(ctx, "movies", "ns1", DeleteByFilterOptions{FilterExpr: map[string]any{"genre": map[string]any{"$in": []string{"a", "b"}}}})
		}, "POST", "vector-indexes/movies/records/delete", `{"namespace":"ns1","filter_expr":{"genre":{"$in":["a","b"]}}}`},
		{"delete by filter, dry run", func(ctx context.Context, s *vi) (m, error) {
			return s.DeleteByFilter(ctx, "movies", "ns1", DeleteByFilterOptions{Filter: map[string]any{"genre": "a"}, DryRun: true})
		}, "POST", "vector-indexes/movies/records/delete", `{"namespace":"ns1","filter":{"genre":"a"},"dry_run":true}`},
		{"delete all sends the confirmation", func(ctx context.Context, s *vi) (m, error) {
			return s.DeleteAll(ctx, "movies", "ns1", DeleteAllOptions{})
		}, "POST", "vector-indexes/movies/records/delete", `{"namespace":"ns1","all":true,"confirm":"ns1"}`},
		{"delete all, dry run", func(ctx context.Context, s *vi) (m, error) {
			return s.DeleteAll(ctx, "movies", "ns1", DeleteAllOptions{DryRun: true})
		}, "POST", "vector-indexes/movies/records/delete", `{"namespace":"ns1","all":true,"confirm":"ns1","dry_run":true}`},
		{"get job", func(ctx context.Context, s *vi) (m, error) {
			return s.GetJob(ctx, "movies", "01JOB")
		}, "GET", "vector-indexes/movies/jobs/01JOB", none},
		{"cancel job", func(ctx context.Context, s *vi) (m, error) {
			return s.CancelJob(ctx, "movies", "01JOB")
		}, "POST", "vector-indexes/movies/jobs/01JOB/cancel", none},
		{"search dense with every option", func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: 5, Vector: []float64{0.1, 0.2, 0.3}, Quality: "exact",
				FilterExpr: map[string]any{"genre": map[string]any{"$eq": "drama"}}, IncludeMetadata: Ptr(true), MetadataFields: []string{"genre"},
				IncludeComponents: Ptr(true), RequireComplete: Ptr(true), TimeoutMS: 250})
		}, "POST", "vector-indexes/movies/search",
			`{"namespace":"ns1","mode":"semantic","k":5,"dense":{"vector":[0.1,0.2,0.3],"quality":"exact"},"filter_expr":{"genre":{"$eq":"drama"}},"include_metadata":true,"metadata_fields":["genre"],"include_components":true,"require_complete":true,"timeout_ms":250}`},
		{"search by record id", func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: 3, RecordID: "a"})
		}, "POST", "vector-indexes/movies/search", `{"namespace":"ns1","mode":"semantic","k":3,"dense":{"record_id":"a"}}`},
		{"search sparse", func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: 2, Sparse: map[string]any{"indices": []int{3}, "values": []float64{0.5}}})
		}, "POST", "vector-indexes/movies/search", `{"namespace":"ns1","mode":"semantic","k":2,"sparse":{"indices":[3],"values":[0.5]}}`},
		{"search keyword", func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: 2, Lexical: map[string]any{"field": "body", "query": "hello"}, Filter: map[string]any{"genre": "a"}})
		}, "POST", "vector-indexes/movies/search", `{"namespace":"ns1","mode":"lexical","k":2,"lexical":{"field":"body","query":"hello"},"filter":{"genre":"a"}}`},
		{"search hybrid", func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: 2, Vector: []float64{1, 0},
				Lexical: map[string]any{"field": "body", "query": "hello"}, Fusion: map[string]any{"method": "rrf", "rrf_k": 60}})
		}, "POST", "vector-indexes/movies/search", `{"namespace":"ns1","mode":"hybrid","k":2,"dense":{"vector":[1,0]},"lexical":{"field":"body","query":"hello"},"fusion":{"method":"rrf","rrf_k":60}}`},
		{"search with a whole dense leg and a chosen mode", func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: 2, Mode: "semantic", Dense: map[string]any{"vector": []float64{1, 0}, "candidates": 50},
				Sparse: map[string]any{"indices": []int{3}, "values": []float64{0.5}}, IncludeValues: Ptr(false)})
		}, "POST", "vector-indexes/movies/search", `{"namespace":"ns1","mode":"semantic","k":2,"dense":{"candidates":50,"vector":[1,0]},"sparse":{"indices":[3],"values":[0.5]},"include_values":false}`},
	}
}

func TestVectorIndexesRequestShapes(t *testing.T) {
	for _, tc := range requestCases() {
		t.Run(tc.name, func(t *testing.T) {
			e := newEngine(t, ok(`{"name":"movies","future_field":{"kept":true},"n":12345678901234567890}`))
			got, err := tc.call(context.Background(), e.vi())
			if err != nil {
				t.Fatal(err)
			}
			assertCall(t, onlyCall(t, e), tc.method, tc.path, tc.body)
			if got["name"] != "movies" || got["future_field"].(map[string]any)["kept"] != true {
				t.Fatalf("answer not returned intact: %v", got)
			}
			if got["n"] != json.Number("12345678901234567890") {
				t.Fatalf("number not kept exactly: %#v", got["n"])
			}
		})
	}
}

func TestVectorIndexesRefuseBadCallsBeforeAnyRequest(t *testing.T) {
	type vi = VectorIndexesService
	type m = map[string]any
	nan := math.NaN()
	both := FetchByFilterOptions{FilterExpr: map[string]any{"a": map[string]any{"$eq": 1}}, Filter: map[string]any{"a": 1}}
	cases := map[string]func(context.Context, *vi) (m, error){
		"empty index name":           func(ctx context.Context, s *vi) (m, error) { return s.DescribeIndex(ctx, "") },
		"dot index name":             func(ctx context.Context, s *vi) (m, error) { return s.DescribeIndex(ctx, ".") },
		"dot-dot index name":         func(ctx context.Context, s *vi) (m, error) { return s.DeleteIndex(ctx, "..") },
		"control character in index": func(ctx context.Context, s *vi) (m, error) { return s.IndexStats(ctx, "a\nb") },
		"empty index on a record call": func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "", "ns1", IndexSearchOptions{K: 1, Vector: []float64{1}})
		},
		"empty job id":   func(ctx context.Context, s *vi) (m, error) { return s.GetJob(ctx, "movies", "") },
		"dot-dot job id": func(ctx context.Context, s *vi) (m, error) { return s.CancelJob(ctx, "movies", "..") },
		"configure without change": func(ctx context.Context, s *vi) (m, error) {
			return s.ConfigureIndex(ctx, "movies", ConfigureIndexOptions{})
		},
		"fetch by filter without a filter": func(ctx context.Context, s *vi) (m, error) {
			return s.FetchByFilter(ctx, "movies", "ns1", FetchByFilterOptions{})
		},
		"fetch by filter with both filters": func(ctx context.Context, s *vi) (m, error) { return s.FetchByFilter(ctx, "movies", "ns1", both) },
		"delete by filter without a filter": func(ctx context.Context, s *vi) (m, error) {
			return s.DeleteByFilter(ctx, "movies", "ns1", DeleteByFilterOptions{})
		},
		"update by filter without a filter": func(ctx context.Context, s *vi) (m, error) {
			return s.UpdateByFilter(ctx, "movies", "ns1", UpdateByFilterOptions{MetadataMerge: map[string]any{"a": 1}})
		},
		"update by filter without a change": func(ctx context.Context, s *vi) (m, error) {
			return s.UpdateByFilter(ctx, "movies", "ns1", UpdateByFilterOptions{FilterExpr: map[string]any{"a": 1}})
		},
		"count with both filters": func(ctx context.Context, s *vi) (m, error) {
			return s.Count(ctx, "movies", "ns1", CountOptions{FilterExpr: map[string]any{"a": 1}, Filter: map[string]any{"a": 1}})
		},
		"update without a change": func(ctx context.Context, s *vi) (m, error) {
			return s.Update(ctx, "movies", "ns1", "a", UpdateOptions{})
		},
		"update with a NaN value": func(ctx context.Context, s *vi) (m, error) {
			return s.Update(ctx, "movies", "ns1", "a", UpdateOptions{Values: []float64{nan}})
		},
		"search without a query": func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: 5})
		},
		"search with vector and record id": func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: 5, Vector: []float64{1}, RecordID: "a"})
		},
		"search with dense and vector": func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: 5, Dense: map[string]any{"vector": []float64{1}}, Vector: []float64{1}})
		},
		"search with dense and quality": func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: 5, Dense: map[string]any{"vector": []float64{1}}, Quality: "exact"})
		},
		"search with quality but no dense query": func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: 5, Quality: "exact", Sparse: map[string]any{"indices": []int{1}, "values": []float64{1}}})
		},
		"search with zero k": func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: 0, Vector: []float64{1}})
		},
		"search with negative k": func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: -1, Vector: []float64{1}})
		},
		"search with both filters": func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: 5, Vector: []float64{1}, FilterExpr: map[string]any{"a": 1}, Filter: map[string]any{"a": 1}})
		},
		"search with a NaN vector": func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: 5, Vector: []float64{nan}})
		},
		"search with an infinite filter value": func(ctx context.Context, s *vi) (m, error) {
			return s.Search(ctx, "movies", "ns1", IndexSearchOptions{K: 5, Vector: []float64{1}, Filter: map[string]any{"a": math.Inf(1)}})
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEngine(t, ok(`{}`))
			got, err := call(context.Background(), e.vi())
			if err == nil || got != nil {
				t.Fatalf("accepted: %v %v", got, err)
			}
			var sdkErr *Error
			if errors.As(err, &sdkErr) {
				t.Fatalf("refusal looks like a server error: %v", err)
			}
			if n := len(e.seen()); n != 0 {
				t.Fatalf("%d requests sent", n)
			}
		})
	}
}

// ── Paging ────────────────────────────────────────────────────────────

func collect[T any](t *testing.T, seq iter.Seq2[T, error]) ([]T, error) {
	t.Helper()
	var out []T
	for item, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, item)
	}
	return out, nil
}

func names(items []map[string]any, key string) []string {
	out := []string{}
	for _, item := range items {
		out = append(out, fmt.Sprint(item[key]))
	}
	return out
}

func TestVectorIndexesIteratorsFollowCursorsToTheEnd(t *testing.T) {
	ctx := context.Background()

	e := newEngine(t, ok(`{"indexes":[{"name":"a"}],"next_cursor":"c1"}`), ok(`{"indexes":[{"name":"b"}],"next_cursor":null}`))
	indexes, err := collect(t, e.vi().AllIndexes(ctx, ListIndexesOptions{Prefix: "x", Limit: 1}))
	if err != nil || strings.Join(names(indexes, "name"), ",") != "a,b" {
		t.Fatalf("indexes %v %v", indexes, err)
	}
	calls := e.seen()
	assertCall(t, calls[0], "POST", "vector-indexes/list", `{"prefix":"x","limit":1}`)
	assertCall(t, calls[1], "POST", "vector-indexes/list", `{"prefix":"x","limit":1,"cursor":"c1"}`)

	e = newEngine(t, ok(`{"namespaces":[{"name":"n1"},{"name":"n2"}],"next_cursor":"c1"}`), ok(`{"namespaces":[]}`))
	spaces, err := collect(t, e.vi().AllNamespaces(ctx, "movies", ListNamespacesOptions{}))
	if err != nil || strings.Join(names(spaces, "name"), ",") != "n1,n2" || len(e.seen()) != 2 {
		t.Fatalf("namespaces %v %v", spaces, err)
	}
	assertCall(t, e.seen()[1], "POST", "vector-indexes/movies/namespaces/list", `{"cursor":"c1"}`)

	e = newEngine(t, ok(`{"ids":["a","b"],"next_cursor":"c1"}`), ok(`{"ids":["c"]}`))
	ids, err := collect(t, e.vi().AllIDs(ctx, "movies", "ns1", ListIDsOptions{Cursor: "start"}))
	if err != nil || strings.Join(ids, ",") != "a,b,c" {
		t.Fatalf("ids %v %v", ids, err)
	}
	assertCall(t, e.seen()[0], "POST", "vector-indexes/movies/records/list-ids", `{"namespace":"ns1","cursor":"start"}`)
	assertCall(t, e.seen()[1], "POST", "vector-indexes/movies/records/list-ids", `{"namespace":"ns1","cursor":"c1"}`)
}

func TestVectorIndexesAllRecordsKeepsTheServerOrder(t *testing.T) {
	// The first page is in an order that is neither sorted nor a map's. An
	// empty page with a cursor does not end the listing.
	order := []string{"b", "r11", "a", "r03", "r07", "z", "r01", "m", "r10", "c0", "r05", "q"}
	first := make([]string, len(order))
	for i, id := range order {
		first[i] = fmt.Sprintf(`%q:{"id":%q}`, id, id)
	}
	first[0] = `"b":{"id":"b","n":1}`
	e := newEngine(t,
		ok(`{"records":{`+strings.Join(first, ",")+`},"next_cursor":"f1"}`),
		ok(`{"records":{},"next_cursor":"f2"}`),
		ok(`{"records":{"c":{"id":"c"}},"namespace":"ns1"}`))
	filter := map[string]any{"genre": map[string]any{"$eq": "drama"}}
	records, err := collect(t, e.vi().AllRecords(context.Background(), "movies", "ns1", FetchByFilterOptions{FilterExpr: filter, Limit: 2, IncludeValues: Ptr(true)}))
	if want := strings.Join(append(order, "c"), ","); err != nil || strings.Join(names(records, "id"), ",") != want {
		t.Fatalf("records %v %v, want %s", names(records, "id"), err, want)
	}
	if records[0]["n"] != json.Number("1") {
		t.Fatalf("record numbers are json.Number: %#v", records[0]["n"])
	}
	calls := e.seen()
	if len(calls) != 3 {
		t.Fatalf("%d requests", len(calls))
	}
	assertCall(t, calls[0], "POST", "vector-indexes/movies/records/fetch-by-filter", `{"namespace":"ns1","filter_expr":{"genre":{"$eq":"drama"}},"limit":2,"include_values":true}`)
	assertCall(t, calls[2], "POST", "vector-indexes/movies/records/fetch-by-filter", `{"namespace":"ns1","filter_expr":{"genre":{"$eq":"drama"}},"limit":2,"cursor":"f2","include_values":true}`)

	// A list of records is accepted too.
	e = newEngine(t, ok(`{"records":[{"id":"x"},{"id":"y"}]}`))
	records, err = collect(t, e.vi().AllRecords(context.Background(), "movies", "ns1", FetchByFilterOptions{Filter: map[string]any{"a": 1}}))
	if err != nil || strings.Join(names(records, "id"), ",") != "x,y" {
		t.Fatalf("records %v %v", records, err)
	}
}

func TestVectorIndexesBadPagesAreProtocolErrors(t *testing.T) {
	// A third request is answered with an error, so a pager that does not stop
	// fails fast instead of reading the same page forever.
	cases := map[string]answer{
		"a repeated cursor":       ok(`{"ids":["a"],"next_cursor":"same"}`),
		"an empty cursor":         ok(`{"ids":["a"],"next_cursor":""}`),
		"a cursor of a number":    ok(`{"ids":["a"],"next_cursor":7}`),
		"no ids":                  ok(`{"next_cursor":"c"}`),
		"an id that is no string": ok(`{"ids":[1]}`),
	}
	for name, page := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEngine(t, page, page, answer{status: 500, body: `{}`})
			_, err := collect(t, e.vi().AllIDs(context.Background(), "movies", "ns1", ListIDsOptions{}))
			var failure *Error
			if !errors.As(err, &failure) || failure.Kind != "protocol" {
				t.Fatalf("expected a protocol error, got %v", err)
			}
			if want := map[string]int{"a repeated cursor": 2}[name]; want != 0 && len(e.seen()) != want {
				t.Fatalf("%d requests, want %d", len(e.seen()), want)
			}
		})
	}
	e := newEngine(t, ok(`{"records":{"a":7}}`))
	if _, err := collect(t, e.vi().AllRecords(context.Background(), "movies", "ns1", FetchByFilterOptions{Filter: map[string]any{"a": 1}})); err == nil {
		t.Fatal("accepted a record that is not an object")
	}
	e = newEngine(t, ok(`{"indexes":["a"]}`))
	if _, err := collect(t, e.vi().AllIndexes(context.Background(), ListIndexesOptions{})); err == nil {
		t.Fatal("accepted an index that is not an object")
	}
}

func TestVectorIndexesBreakingAnIteratorStopsRequests(t *testing.T) {
	e := newEngine(t, ok(`{"ids":["a","b"],"next_cursor":"c1"}`))
	for id, err := range e.vi().AllIDs(context.Background(), "movies", "ns1", ListIDsOptions{}) {
		if err != nil || id != "a" {
			t.Fatal(id, err)
		}
		break
	}
	if n := len(e.seen()); n != 1 {
		t.Fatalf("%d requests after break", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e = newEngine(t, ok(`{"ids":["a","b"],"next_cursor":"c1"}`))
	var got []string
	var stop error
	for id, err := range e.vi().AllIDs(ctx, "movies", "ns1", ListIDsOptions{}) {
		if err != nil {
			stop = err
			break
		}
		got = append(got, id)
		cancel()
	}
	if strings.Join(got, ",") != "a" || !errors.Is(stop, context.Canceled) || len(e.seen()) != 1 {
		t.Fatalf("cancellation did not stop iteration: %v %v %d", got, stop, len(e.seen()))
	}
}

// ── Upsert ────────────────────────────────────────────────────────────

// echo answers each upsert with the number of records it carried; failAt
// answers that request number with a capacity error instead.
func echo(t *testing.T, failAt int) *engine {
	e := newEngine(t)
	e.setHandle(func(n int, call seen, w http.ResponseWriter) bool {
		w.Header().Set("Content-Type", "application/json")
		if n == failAt {
			w.WriteHeader(409)
			_, _ = io.WriteString(w, `{"error":{"code":"configuration_limit_exceeded","message":"this index holds the most vectors it may hold","status":409,"retryable":false,"details":{"reason":"exact_index_capacity","limit":1000}}}`)
			return true
		}
		var body struct{ Records []json.RawMessage }
		if err := json.Unmarshal([]byte(call.body), &body); err != nil {
			t.Errorf("upsert body is not JSON: %v", err)
		}
		_, _ = fmt.Fprintf(w, `{"upserted_count":%d,"namespace_created":%t,"receipt":{"n":%d}}`, len(body.Records), n == 1, len(body.Records))
		return true
	})
	return e
}

func sentIDs(t *testing.T, calls []seen) ([]string, []int) {
	t.Helper()
	var ids []string
	var perCall []int
	for _, call := range calls {
		var body struct {
			Namespace string
			Records   []struct{ ID string }
		}
		if err := json.Unmarshal([]byte(call.body), &body); err != nil {
			t.Fatal(err)
		}
		perCall = append(perCall, len(body.Records))
		for _, r := range body.Records {
			ids = append(ids, r.ID)
		}
	}
	return ids, perCall
}

func records(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = map[string]any{"id": fmt.Sprintf("r%04d", i)}
	}
	return out
}

func TestVectorIndexesUpsert(t *testing.T) {
	e := echo(t, 0)
	out, err := e.vi().Upsert(context.Background(), "movies", "ns1", FromSlice([]map[string]any{
		{"id": "a", "values": []float64{1, 0}, "metadata": map[string]any{"n": 1, "tag": "<x>"}},
		{"id": "b", "sparse_values": map[string]any{"indices": []int{3}, "values": []float64{0.5}}},
	}), UpsertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertCall(t, onlyCall(t, e), "POST", "vector-indexes/movies/records/upsert",
		`{"namespace":"ns1","records":[{"id":"a","metadata":{"n":1,"tag":"<x>"},"values":[1,0]},{"id":"b","sparse_values":{"indices":[3],"values":[0.5]}}]}`)
	if out.UpsertedCount != 2 || !out.NamespaceCreated || out.Batches != 1 || len(out.Receipts) != 1 {
		t.Fatalf("total %+v", out)
	}
	if out.Receipts[0].(map[string]any)["n"] != json.Number("2") {
		t.Fatalf("receipt %#v", out.Receipts[0])
	}
}

func TestVectorIndexesUpsertSplitsAtTheRecordCap(t *testing.T) {
	e := echo(t, 0)
	out, err := e.vi().Upsert(context.Background(), "movies", "ns1", FromSlice(records(2500)), UpsertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ids, perCall := sentIDs(t, e.seen())
	if fmt.Sprint(perCall) != "[1000 1000 500]" || out.Batches != 3 || out.UpsertedCount != 2500 {
		t.Fatalf("batches %v %+v", perCall, out)
	}
	for i, id := range ids {
		if id != fmt.Sprintf("r%04d", i) {
			t.Fatalf("record %d sent as %s", i, id)
		}
	}
	e = echo(t, 0)
	if _, err = e.vi().Upsert(context.Background(), "movies", "ns1", FromSlice(records(5)), UpsertOptions{BatchSize: 2}); err != nil {
		t.Fatal(err)
	}
	if _, perCall = sentIDs(t, e.seen()); fmt.Sprint(perCall) != "[2 2 1]" {
		t.Fatalf("batches %v", perCall)
	}
}

func TestVectorIndexesUpsertSplitsAtTheByteBound(t *testing.T) {
	input := make([]map[string]any, 40)
	for i := range input {
		input[i] = map[string]any{"id": fmt.Sprintf("r%02d", i), "pad": strings.Repeat("é", 50+i*7)}
	}
	const limit = 2000
	e := echo(t, 0)
	if _, err := e.vi().Upsert(context.Background(), "movies", "ns1", FromSlice(input), UpsertOptions{MaxBytes: limit}); err != nil {
		t.Fatal(err)
	}
	calls := e.seen()
	ids, _ := sentIDs(t, calls)
	if len(ids) != len(input) || len(calls) < 3 {
		t.Fatalf("%d records in %d requests", len(ids), len(calls))
	}
	for i, call := range calls {
		if len(call.body) > limit {
			t.Fatalf("request %d is %d bytes", i, len(call.body))
		}
		// Greedy: the next request's first record did not fit in this one.
		if i+1 < len(calls) {
			var next struct{ Records []json.RawMessage }
			_ = json.Unmarshal([]byte(calls[i+1].body), &next)
			if len(call.body)+1+len(next.Records[0]) <= limit {
				t.Fatalf("request %d stopped early", i)
			}
		}
	}

	// A body exactly at the bound is one request; one byte less is two.
	three := records(3)
	body := `{"namespace":"ns1","records":[{"id":"r0000"},{"id":"r0001"},{"id":"r0002"}]}`
	e = echo(t, 0)
	if _, err := e.vi().Upsert(context.Background(), "movies", "ns1", FromSlice(three), UpsertOptions{MaxBytes: len(body)}); err != nil {
		t.Fatal(err)
	}
	if calls = e.seen(); len(calls) != 1 || calls[0].body != body {
		t.Fatalf("%d requests: %v", len(calls), calls)
	}
	e = echo(t, 0)
	if _, err := e.vi().Upsert(context.Background(), "movies", "ns1", FromSlice(three), UpsertOptions{MaxBytes: len(body) - 1}); err != nil {
		t.Fatal(err)
	}
	if n := len(e.seen()); n != 2 {
		t.Fatalf("%d requests", n)
	}
}

func batchError(t *testing.T, err error, offset int64, count int, outcome string) *BatchWriteError {
	t.Helper()
	var failure *BatchWriteError
	if !errors.As(err, &failure) {
		t.Fatalf("expected a BatchWriteError, got %v", err)
	}
	if failure.Offset != offset || failure.Count != count || failure.Outcome != outcome {
		t.Fatalf("got offset %d count %d %s, want %d %d %s", failure.Offset, failure.Count, failure.Outcome, offset, count, outcome)
	}
	return failure
}

func TestVectorIndexesUpsertFailureReportsTheUnacknowledgedRange(t *testing.T) {
	e := echo(t, 2)
	out, err := e.vi().Upsert(context.Background(), "movies", "ns1", FromSlice(records(5)), UpsertOptions{BatchSize: 2})
	failure := batchError(t, err, 2, 2, "unknown")
	var cause *Error
	if !errors.As(failure, &cause) || cause.Code != "configuration_limit_exceeded" || cause.Reason != "exact_index_capacity" || cause.Status != 409 {
		t.Fatalf("cause %#v", cause)
	}
	if out == nil || out.UpsertedCount != 2 || out.Batches != 1 {
		t.Fatalf("acknowledged total %+v", out)
	}
	// r0004 was never sent.
	if ids, _ := sentIDs(t, e.seen()); strings.Join(ids, ",") != "r0000,r0001,r0002,r0003" {
		t.Fatalf("sent %v", ids)
	}
}

func TestVectorIndexesUpsertBatchesYieldEachAcknowledgedRange(t *testing.T) {
	e := echo(t, 0)
	var got []string
	for batch, err := range e.vi().UpsertBatches(context.Background(), "movies", "ns1", FromSlice(records(5)), UpsertOptions{BatchSize: 2}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d+%d=%d/%t", batch.Offset, batch.Count, batch.UpsertedCount, batch.NamespaceCreated))
	}
	if strings.Join(got, " ") != "0+2=2/true 2+2=2/false 4+1=1/false" {
		t.Fatalf("batches %v", got)
	}

	// Breaking the loop stops reading the input and sending requests.
	read := 0
	source := func(yield func(map[string]any, error) bool) {
		for _, record := range records(5) {
			read++
			if !yield(record, nil) {
				return
			}
		}
	}
	e = echo(t, 0)
	for range e.vi().UpsertBatches(context.Background(), "movies", "ns1", source, UpsertOptions{BatchSize: 1}) {
		break
	}
	if read != 1 || len(e.seen()) != 1 {
		t.Fatalf("read %d records and sent %d requests after break", read, len(e.seen()))
	}
}

func TestVectorIndexesUpsertRefusesWhatCannotBeSent(t *testing.T) {
	huge := map[string]any{"id": "big", "pad": strings.Repeat("x", VectorUpsertMaxBodyBytes)}
	cases := []struct {
		name    string
		input   []map[string]any
		options UpsertOptions
		offset  int64
		count   int
		sent    int
	}{
		{"an oversized record in the first batch", []map[string]any{{"id": "a"}, {"id": "b"}, huge}, UpsertOptions{}, 0, 3, 0},
		{"an oversized record after a sent batch", []map[string]any{{"id": "a"}, {"id": "b"}, huge}, UpsertOptions{BatchSize: 2}, 2, 1, 1},
		{"a NaN value", []map[string]any{{"id": "a", "values": []float64{math.NaN()}}}, UpsertOptions{}, 0, 1, 0},
		{"a value JSON cannot carry", []map[string]any{{"id": "a"}, {"id": "b", "x": make(chan int)}}, UpsertOptions{}, 0, 2, 0},
		{"a nil record", []map[string]any{{"id": "a"}, nil}, UpsertOptions{}, 0, 2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo(t, 0)
			_, err := e.vi().Upsert(context.Background(), "movies", "ns1", FromSlice(tc.input), tc.options)
			batchError(t, err, tc.offset, tc.count, "not_sent")
			if n := len(e.seen()); n != tc.sent {
				t.Fatalf("%d requests, want %d", n, tc.sent)
			}
		})
	}

	// A failing input reports the records read but not sent.
	inputErr := errors.New("source failed")
	source := func(yield func(map[string]any, error) bool) {
		if yield(map[string]any{"id": "a"}, nil) && yield(map[string]any{"id": "b"}, nil) {
			yield(nil, inputErr)
		}
	}
	e := echo(t, 0)
	_, err := e.vi().Upsert(context.Background(), "movies", "ns1", source, UpsertOptions{})
	if failure := batchError(t, err, 0, 2, "not_sent"); !errors.Is(failure, inputErr) || len(e.seen()) != 0 {
		t.Fatalf("cause %v, %d requests", failure.Unwrap(), len(e.seen()))
	}

	// A canceled context sends nothing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e = echo(t, 0)
	_, err = e.vi().Upsert(ctx, "movies", "ns1", FromSlice(records(1)), UpsertOptions{})
	if failure := batchError(t, err, 0, 1, "not_sent"); !errors.Is(failure, context.Canceled) || len(e.seen()) != 0 {
		t.Fatalf("cause %v, %d requests", failure.Unwrap(), len(e.seen()))
	}
}

func TestVectorIndexesUpsertChecksOptionsBeforeAnyRequest(t *testing.T) {
	for _, options := range []UpsertOptions{{BatchSize: -1}, {BatchSize: 1001}, {MaxBytes: 2_000_001}, {MaxBytes: 10}, {MaxBytes: -5}} {
		e := echo(t, 0)
		out, err := e.vi().Upsert(context.Background(), "movies", "ns1", FromSlice(records(1)), options)
		var failure *BatchWriteError
		if err == nil || errors.As(err, &failure) || out.Batches != 0 || len(e.seen()) != 0 {
			t.Fatalf("options %+v: %v, %d requests", options, err, len(e.seen()))
		}
	}
	e := echo(t, 0)
	if _, err := e.vi().Upsert(context.Background(), "movies", "ns1", nil, UpsertOptions{}); err == nil {
		t.Fatal("accepted nil records")
	}
	if _, err := e.vi().Upsert(context.Background(), "..", "ns1", FromSlice(records(1)), UpsertOptions{}); err == nil {
		t.Fatal("accepted an unusable index name")
	}
	// Nothing to write sends nothing.
	out, err := e.vi().Upsert(context.Background(), "movies", "ns1", FromSlice[map[string]any](nil), UpsertOptions{})
	if err != nil || out.Batches != 0 || out.UpsertedCount != 0 || len(e.seen()) != 0 {
		t.Fatalf("empty upsert: %+v %v %d", out, err, len(e.seen()))
	}
}

func TestVectorIndexesUpsertAnswerMustCarryACount(t *testing.T) {
	for _, body := range []string{`{"receipt":{}}`, `{"upserted_count":3}`, `{"upserted_count":-1}`, `{"upserted_count":1.5}`, `[]`} {
		e := newEngine(t, ok(body))
		_, err := e.vi().Upsert(context.Background(), "movies", "ns1", FromSlice(records(2)), UpsertOptions{})
		failure := batchError(t, err, 0, 2, "unknown")
		var cause *Error
		if !errors.As(failure, &cause) || cause.Kind != "protocol" {
			t.Fatalf("%s: cause %v", body, failure.Unwrap())
		}
	}
}

func TestVectorIndexesUpsertEncodesTheNamespaceSafely(t *testing.T) {
	e := echo(t, 0)
	if _, err := e.vi().Upsert(context.Background(), "movies", `n"s<é>`, FromSlice(records(1)), UpsertOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := onlyCall(t, e).body; got != `{"namespace":"n\"s<é>","records":[{"id":"r0000"}]}` {
		t.Fatalf("body %s", got)
	}
}

// ── Errors ────────────────────────────────────────────────────────────

func TestVectorIndexesErrorMapping(t *testing.T) {
	cases := []struct {
		status    int
		code      string
		retryable bool
		details   string
		kind      string
	}{
		{400, "invalid_argument", false, `{"limit":10000,"reason":"k_out_of_range"}`, "http"},
		{400, "unknown_request_field", false, `{"unknown":["/dense/bogus"]}`, "http"},
		{401, "unauthorized", false, "", "authentication"},
		{403, "key_scope_insufficient", false, "", "permission"},
		{404, "index_not_found", false, "", "http"},
		{404, "record_not_found", false, "", "http"},
		{409, "index_already_exists", false, `{"reason":"index_already_exists"}`, "http"},
		{409, "index_capability_missing", false, `{"reason":"sparse_not_declared"}`, "http"},
		{409, "record_write_conflict", true, "", "http"},
		{413, "request_too_large", false, "", "http"},
		{422, "filtered_search_incomplete", false, "", "http"},
		{429, "overloaded", true, "", "rate_limit"},
		{501, "not_implemented", false, `{"reason":"record_api_not_enabled"}`, "http"},
		{501, "not_implemented", false, `{"reason":"ivf_pq_index_rebuild"}`, "http"},
		{503, "record_api_restore_pending", true, "", "http"},
	}
	const requestID = "0190b3e2-8f4a-7c1d-b5e6-0123456789ab"
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d %s %s", tc.status, tc.code, tc.details), func(t *testing.T) {
			a := envelope(tc.status, tc.code, tc.retryable, tc.details)
			a.headers = map[string]string{"X-OC-Request-Id": requestID, "Retry-After": "7"}
			e := newEngine(t, a)
			_, err := e.vi().Search(context.Background(), "movies", "ns1", IndexSearchOptions{K: 5, Vector: []float64{1}})
			var failure *Error
			if !errors.As(err, &failure) {
				t.Fatalf("expected *Error, got %v", err)
			}
			if failure.Kind != tc.kind || failure.Status != tc.status || failure.Code != tc.code || failure.Retryable != tc.retryable {
				t.Fatalf("got %s %d %s %t", failure.Kind, failure.Status, failure.Code, failure.Retryable)
			}
			details := ""
			wantReason := ""
			if failure.Details != nil {
				raw, _ := json.Marshal(failure.Details)
				details = string(raw)
			}
			if tc.details != "" {
				var parsed struct{ Reason string }
				_ = json.Unmarshal([]byte(tc.details), &parsed)
				wantReason = parsed.Reason
			}
			if details != tc.details || failure.Reason != wantReason {
				t.Fatalf("details %s reason %q", details, failure.Reason)
			}
			if failure.Message != "message for "+tc.code || strings.Contains(failure.Error(), "message for") {
				t.Fatalf("message %q, Error() %q", failure.Message, failure.Error())
			}
			if failure.RequestID != requestID || failure.RetryAfter != "7" {
				t.Fatalf("request id %q retry after %q", failure.RequestID, failure.RetryAfter)
			}
			notEnabled := tc.status == 501 && wantReason == "record_api_not_enabled"
			if errors.Is(err, ErrRecordAPINotEnabled) != notEnabled {
				t.Fatalf("errors.Is(ErrRecordAPINotEnabled) = %t", !notEnabled)
			}
			if n := len(e.seen()); n != 1 {
				t.Fatalf("%d requests: no retries by default", n)
			}
		})
	}
}

func TestVectorIndexesRecordAPINotEnabledThroughABatchError(t *testing.T) {
	e := newEngine(t, envelope(501, "not_implemented", false, `{"reason":"record_api_not_enabled"}`))
	_, err := e.vi().Upsert(context.Background(), "movies", "ns1", FromSlice(records(1)), UpsertOptions{})
	if !errors.Is(err, ErrRecordAPINotEnabled) {
		t.Fatalf("not matched through BatchWriteError: %v", err)
	}
	if errors.Is(&Error{Kind: "http", Status: 500, Reason: "record_api_not_enabled"}, ErrRecordAPINotEnabled) {
		t.Fatal("matched a status other than 501")
	}
}

func TestVectorIndexesFlatAndPartialErrorBodies(t *testing.T) {
	e := newEngine(t, answer{status: 400, body: `{"error":"bad thing","code":"flat_code"}`})
	_, err := e.vi().DescribeIndex(context.Background(), "movies")
	var failure *Error
	if !errors.As(err, &failure) || failure.Message != "bad thing" || failure.Code != "flat_code" ||
		failure.Reason != "" || failure.Details != nil || failure.Retryable {
		t.Fatalf("flat body: %#v", failure)
	}
	e = newEngine(t, answer{status: 404, body: `{"error":{"code":"job_not_found"}}`})
	_, err = e.vi().GetJob(context.Background(), "movies", "01JOB")
	if !errors.As(err, &failure) || failure.Message != "job_not_found" || failure.Code != "job_not_found" {
		t.Fatalf("envelope without message: %#v", failure)
	}
	e = newEngine(t, answer{status: 307, body: `{}`, headers: map[string]string{"Location": testPrefix + "/elsewhere"}})
	_, err = e.vi().DescribeIndex(context.Background(), "movies")
	if !errors.As(err, &failure) || failure.Status != 307 || len(e.seen()) != 1 {
		t.Fatalf("redirect: %v, %d requests", err, len(e.seen()))
	}
}

// ── Retries ───────────────────────────────────────────────────────────

// noWaiting records the retry waits instead of sleeping.
func noWaiting(t *testing.T) *[]time.Duration {
	var waits []time.Duration
	var mu sync.Mutex
	sleep, backoff := retrySleep, retryBackoff
	retrySleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, d)
		return nil
	}
	retryBackoff = func(attempt int) time.Duration { return time.Duration(attempt+1) * time.Millisecond }
	t.Cleanup(func() { retrySleep, retryBackoff = sleep, backoff })
	return &waits
}

func TestVectorIndexesReadsAreRetriedWhenEnabled(t *testing.T) {
	waits := noWaiting(t)
	limited := envelope(429, "overloaded", true, "")
	limited.headers = map[string]string{"Retry-After": "3"}
	e := newEngine(t, limited, ok(`{"returned":1,"hits":[]}`))
	var events []RequestEvent
	c := e.client(Config{MaxRetries: 1, OnRequest: func(event RequestEvent) { events = append(events, event) }})
	out, err := c.VectorIndexes.Search(context.Background(), "movies", "ns1", IndexSearchOptions{K: 5, Vector: []float64{1}})
	if err != nil || out["returned"] != json.Number("1") {
		t.Fatalf("%v %v", out, err)
	}
	calls := e.seen()
	if len(calls) != 2 || fmt.Sprint(*waits) != "[3s]" {
		t.Fatalf("%d requests, waits %v", len(calls), *waits)
	}
	for _, call := range calls {
		assertCall(t, call, "POST", "vector-indexes/movies/search", `{"namespace":"ns1","mode":"semantic","k":5,"dense":{"vector":[1]}}`)
	}
	if calls[0].header.Get("X-OC-Logical-Request-Id") != calls[1].header.Get("X-OC-Logical-Request-Id") {
		t.Fatal("a retry changed the logical request id")
	}
	if len(events) != 1 || events[0].Status != 200 || events[0].ErrorKind != "" {
		t.Fatalf("observer events %+v", events)
	}
}

func TestVectorIndexesRetriesUseBackoffAndStopAtTheLimit(t *testing.T) {
	waits := noWaiting(t)
	e := newEngine(t, envelope(503, "unavailable", true, ""))
	_, err := e.client(Config{MaxRetries: 2}).VectorIndexes.DescribeIndex(context.Background(), "movies")
	var failure *Error
	if !errors.As(err, &failure) || failure.Status != 503 || len(e.seen()) != 3 || fmt.Sprint(*waits) != "[1ms 2ms]" {
		t.Fatalf("%v, %d requests, waits %v", err, len(e.seen()), *waits)
	}
	*waits = nil
	for _, status := range []int{500, 502, 504} {
		e = newEngine(t, answer{status: status, body: `{}`}, ok(`{"name":"movies"}`))
		if _, err := e.client(Config{MaxRetries: 1}).VectorIndexes.DescribeIndex(context.Background(), "movies"); err != nil || len(e.seen()) != 2 {
			t.Fatalf("%d: %v, %d requests", status, err, len(e.seen()))
		}
	}
}

func TestVectorIndexesNotRetried(t *testing.T) {
	noWaiting(t)
	for _, a := range []answer{
		envelope(400, "invalid_argument", false, ""),
		envelope(404, "index_not_found", false, ""),
		envelope(409, "record_write_conflict", true, ""),
		envelope(501, "not_implemented", false, `{"reason":"record_api_not_enabled"}`),
	} {
		e := newEngine(t, a)
		if _, err := e.client(Config{MaxRetries: 3}).VectorIndexes.DescribeIndex(context.Background(), "movies"); err == nil || len(e.seen()) != 1 {
			t.Fatalf("status %d: %v, %d requests", a.status, err, len(e.seen()))
		}
	}
	// A wait past the retry budget is not taken.
	for _, tc := range []struct {
		after  string
		config Config
	}{
		{"3600", Config{MaxRetries: 3}},
		{"2", Config{MaxRetries: 3, MaxRetryTime: time.Second}},
	} {
		waits := noWaiting(t)
		a := envelope(503, "unavailable", true, "")
		a.headers = map[string]string{"Retry-After": tc.after}
		e := newEngine(t, a, ok(`{}`))
		if _, err := e.client(tc.config).VectorIndexes.DescribeIndex(context.Background(), "movies"); err == nil || len(e.seen()) != 1 || len(*waits) != 0 {
			t.Fatalf("Retry-After %s: %v, %d requests, waits %v", tc.after, err, len(e.seen()), *waits)
		}
	}
	// The other services' operations are sent once.
	e := newEngine(t, envelope(503, "unavailable", true, ""))
	if _, err := e.client(Config{MaxRetries: 3}).Rows.Get(context.Background(), "demo.items", "a"); err == nil || len(e.seen()) != 1 {
		t.Fatalf("rows get: %v, %d requests", err, len(e.seen()))
	}
}

func TestVectorIndexesEveryReadRetriesAndNoWriteDoes(t *testing.T) {
	noWaiting(t)
	reads := map[string]bool{
		"describe index": true, "describe index escapes the name": true, "list indexes": true, "list indexes by name": true,
		"list indexes without a filter sends an empty object": true, "index statistics": true, "describe namespace": true,
		"list namespaces": true, "fetch": true, "fetch can turn metadata off": true, "fetch without ids sends an empty list": true,
		"fetch by filter expression": true, "fetch by equality filter": true, "list ids": true, "count with a filter, unescaped": true,
		"count everything": true, "get job": true,
	}
	for _, tc := range requestCases() {
		read := reads[tc.name] || strings.HasPrefix(tc.name, "search")
		t.Run(tc.name, func(t *testing.T) {
			e := newEngine(t, envelope(503, "unavailable", true, ""), ok(`{"upserted_count":1}`))
			_, err := tc.call(context.Background(), e.client(Config{MaxRetries: 3}).VectorIndexes)
			calls := e.seen()
			if read && (err != nil || len(calls) != 2) {
				t.Fatalf("read: %v, %d requests", err, len(calls))
			}
			if !read && (err == nil || len(calls) != 1) {
				t.Fatalf("write: %v, %d requests", err, len(calls))
			}
			for _, call := range calls {
				assertCall(t, call, tc.method, tc.path, tc.body)
			}
		})
	}
	e := newEngine(t, envelope(503, "unavailable", true, ""), ok(`{"upserted_count":1}`))
	_, err := e.client(Config{MaxRetries: 3}).VectorIndexes.Upsert(context.Background(), "movies", "ns1", FromSlice(records(1)), UpsertOptions{})
	batchError(t, err, 0, 1, "unknown")
	if len(e.seen()) != 1 {
		t.Fatalf("upsert sent %d requests", len(e.seen()))
	}
}

func TestVectorIndexesTransportFailures(t *testing.T) {
	noWaiting(t)
	drop := func(n int, _ seen, w http.ResponseWriter) bool {
		if n > 1 {
			return false
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
		return true
	}
	e := newEngine(t, ok(`{"count":1}`))
	e.setHandle(drop)
	out, err := e.client(Config{MaxRetries: 1}).VectorIndexes.Count(context.Background(), "movies", "ns1", CountOptions{})
	if err != nil || out["count"] != json.Number("1") || len(e.seen()) != 2 {
		t.Fatalf("read after a dropped connection: %v %v, %d requests", out, err, len(e.seen()))
	}
	e = newEngine(t, ok(`{"deleted":1}`))
	e.setHandle(drop)
	_, err = e.client(Config{MaxRetries: 1}).VectorIndexes.Delete(context.Background(), "movies", "ns1", []string{"a"})
	var failure *Error
	if !errors.As(err, &failure) || failure.Kind != "transport" || len(e.seen()) != 1 {
		t.Fatalf("write after a dropped connection: %v, %d requests", err, len(e.seen()))
	}
}

func TestVectorIndexesCancellationDuringARetryWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sleep := retrySleep
	retrySleep = func(waitCtx context.Context, _ time.Duration) error {
		cancel()
		<-waitCtx.Done()
		return waitCtx.Err()
	}
	t.Cleanup(func() { retrySleep = sleep })
	e := newEngine(t, envelope(503, "unavailable", true, ""), ok(`{}`))
	_, err := e.client(Config{MaxRetries: 3}).VectorIndexes.DescribeIndex(ctx, "movies")
	var failure *Error
	if !errors.As(err, &failure) || failure.Kind != "canceled" || !errors.Is(err, context.Canceled) || len(e.seen()) != 1 {
		t.Fatalf("%v, %d requests", err, len(e.seen()))
	}
}

func TestRetryConfigurationIsChecked(t *testing.T) {
	for _, config := range []Config{{MaxRetries: -1}, {MaxRetries: 100}, {MaxRetryTime: -time.Second}} {
		config.URL, config.Token = "https://example.com", "test"
		if _, err := NewClient(config); err == nil {
			t.Fatalf("accepted %+v", config)
		}
	}
	c, err := NewClient(Config{URL: "https://example.com", Token: "test", MaxRetries: 99})
	if err != nil || c.retries != 99 || c.retryTime != 30*time.Second {
		t.Fatalf("%v %+v", err, c)
	}
}

func TestRetryAfterAndBackoff(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"3": 3 * time.Second, "0": 0, " 007 ": 7 * time.Second,
		"Fri, 13 Nov 2026 12:00:09 GMT": 9 * time.Second,
		"Fri, 13 Nov 2026 11:00:00 GMT": 0,
	}
	for value, want := range cases {
		if got, ok := parseRetryAfter(value, now); !ok || got != want {
			t.Errorf("%q: %v %t, want %v", value, got, ok, want)
		}
	}
	if got, ok := parseRetryAfter("99999999999999999999999", now); !ok || got < 1000*time.Hour {
		t.Errorf("a huge delay must saturate, got %v %t", got, ok)
	}
	for _, value := range []string{"", "soon", "-1", "1.5"} {
		if _, ok := parseRetryAfter(value, now); ok {
			t.Errorf("accepted %q", value)
		}
	}
	for attempt := 0; attempt < 12; attempt++ {
		limit := min(4*time.Second, 250*time.Millisecond<<min(attempt, 4))
		for i := 0; i < 50; i++ {
			if d := retryBackoff(attempt); d < 0 || d >= limit {
				t.Fatalf("attempt %d: backoff %v outside [0, %v)", attempt, d, limit)
			}
		}
	}
}

func TestFromSlice(t *testing.T) {
	var got []int
	for value, err := range FromSlice([]int{1, 2, 3}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, value)
		if value == 2 {
			break
		}
	}
	if fmt.Sprint(got) != "[1 2]" {
		t.Fatal(got)
	}
}
