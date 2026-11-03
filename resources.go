package originchaindb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// SchemasService reads and registers TOML schemas.
type SchemasService struct{ client *Client }

func (s *SchemasService) List(ctx context.Context) (any, error) {
	return s.client.request(ctx, http.MethodGet, "schemas", nil, nil, nil)
}
func (s *SchemasService) Get(ctx context.Context, name string) (any, error) {
	p, e := path("schemas", name)
	if e != nil {
		return nil, e
	}
	return s.client.request(ctx, http.MethodGet, p, nil, nil, nil)
}
func (s *SchemasService) Register(ctx context.Context, toml string) (any, error) {
	return s.client.request(ctx, http.MethodPost, "schemas", nil, nil, &toml)
}

// RowsService operates on rows in a schema. Batch reports are returned intact.
type RowsService struct{ client *Client }

func (s *RowsService) Get(ctx context.Context, schema, key string) (map[string]any, error) {
	p, e := path("rows", schema, key)
	if e != nil {
		return nil, e
	}
	return objectResult(s.client.request(ctx, http.MethodGet, p, nil, nil, nil))
}
func (s *RowsService) Put(ctx context.Context, schema string, row map[string]any) (map[string]any, error) {
	p, e := path("rows", schema)
	if e != nil {
		return nil, e
	}
	return objectResult(s.client.request(ctx, http.MethodPost, p, nil, row, nil))
}
func (s *RowsService) PutBatch(ctx context.Context, schema string, rows []map[string]any) (map[string]any, error) {
	p, e := path("rows", schema, "_batch")
	if e != nil {
		return nil, e
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	return objectResult(s.client.request(ctx, http.MethodPost, p, nil, rows, nil))
}

type VectorService struct{ client *Client }

// VectorSearchOptions controls server-side vector search. Filter is sent intact.
type VectorSearchOptions struct {
	K      int
	Metric string
	Filter map[string]any
}

func (s *VectorService) Put(ctx context.Context, table, id string, embedding []float64, metadata map[string]any) (any, error) {
	p, e := path("vector", table, "put")
	if e != nil {
		return nil, e
	}
	if len(embedding) == 0 {
		return nil, errors.New("embedding must not be empty")
	}
	body := map[string]any{"id": id, "embedding": embedding, "dim": len(embedding)}
	if metadata != nil {
		body["metadata"] = metadata
	}
	return s.client.request(ctx, http.MethodPost, p, nil, body, nil)
}
func (s *VectorService) Search(ctx context.Context, table string, query []float64, options VectorSearchOptions) ([]VectorHit, error) {
	p, e := path("vector", table, "topk")
	if e != nil {
		return nil, e
	}
	if len(query) == 0 {
		return nil, errors.New("query vector must not be empty")
	}
	if options.K == 0 {
		options.K = 10
	}
	if options.K < 0 {
		return nil, errors.New("k must be positive")
	}
	if options.Metric == "" {
		options.Metric = "cosine"
	}
	body := map[string]any{"query": query, "dim": len(query), "k": options.K, "metric": options.Metric}
	if options.Filter != nil {
		body["filter"] = options.Filter
	}
	value, err := s.client.request(ctx, http.MethodPost, p, nil, body, nil)
	if err != nil {
		return nil, err
	}
	return decodeVector(value)
}
func (s *VectorService) Delete(ctx context.Context, table, id string) (any, error) {
	p, e := path("vector", table, id)
	if e != nil {
		return nil, e
	}
	return s.client.request(ctx, http.MethodDelete, p, nil, nil, nil)
}

type FTSService struct{ client *Client }
type FTSSearchOptions struct {
	Mode      string
	K         int
	Highlight bool
	Fuzzy     int
	Facets    []string
}

func (s *FTSService) Index(ctx context.Context, table, field, id, text string) (any, error) {
	p, e := path("fts", table, field)
	if e != nil {
		return nil, e
	}
	return s.client.request(ctx, http.MethodPost, p, nil, map[string]any{"doc_id": id, "text": text}, nil)
}
func (s *FTSService) Search(ctx context.Context, table, field, query string, options FTSSearchOptions) (*FTSResult, error) {
	p, e := path("fts", table, field)
	if e != nil {
		return nil, e
	}
	if options.Mode == "" {
		options.Mode = "bm25"
	}
	if options.K == 0 {
		options.K = 10
	}
	if options.K < 1 || (options.Mode != "bm25" && options.Mode != "boolean" && options.Mode != "phrase") {
		return nil, errors.New("invalid full-text search options")
	}
	q := url.Values{"q": {query}, "mode": {options.Mode}}
	if options.Mode == "bm25" {
		q.Set("k", fmt.Sprint(options.K))
	}
	if options.Highlight {
		q.Set("highlight", "true")
	}
	if options.Fuzzy > 0 {
		q.Set("fuzzy", fmt.Sprint(options.Fuzzy))
	}
	if len(options.Facets) > 0 {
		q.Set("facets", joinFacets(options.Facets))
	}
	value, err := s.client.request(ctx, http.MethodGet, p, q, nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeFTS(value)
}
func joinFacets(values []string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}

type GraphService struct{ client *Client }

// Query submits Cypher with named parameters. It never interpolates values.
func (s *GraphService) Query(ctx context.Context, query string, params map[string]any) (any, error) {
	if params == nil {
		params = map[string]any{}
	}
	return s.client.request(ctx, http.MethodPost, "cypher", nil, map[string]any{"cypher": query, "params": params}, nil)
}
func (s *GraphService) Neighbors(ctx context.Context, schema, key, relation string) (any, error) {
	p, e := path("graph", schema, "neighbors")
	if e != nil {
		return nil, e
	}
	return s.client.request(ctx, http.MethodGet, p, url.Values{"pk": {key}, "rel": {relation}}, nil, nil)
}
func (s *GraphService) BFS(ctx context.Context, schema, key, relation string, maxDepth int) (any, error) {
	p, e := path("graph", schema, "bfs")
	if e != nil {
		return nil, e
	}
	if maxDepth < 1 {
		return nil, errors.New("maxDepth must be positive")
	}
	return s.client.request(ctx, http.MethodGet, p, url.Values{"pk": {key}, "rel": {relation}, "max_depth": {fmt.Sprint(maxDepth)}}, nil, nil)
}
