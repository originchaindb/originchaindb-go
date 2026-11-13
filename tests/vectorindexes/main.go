// Consumer for tests/contract/vector_indexes.py: makes each listed call once.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	oc "github.com/originchaindb/originchaindb-go"
)

const requestID = "84f92822-5837-432e-981a-7b24ad65e16c"

func check(ok bool, what string) {
	if !ok {
		panic("contract assertion failed: " + what)
	}
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func object(value any) map[string]any {
	out, _ := value.(map[string]any)
	return out
}

func number(value any) float64 {
	n, ok := value.(json.Number)
	check(ok, "a JSON number")
	f, err := n.Float64()
	check(err == nil, "a JSON number")
	return f
}

func main() {
	ctx := context.Background()
	db, err := oc.FromEnv()
	if err != nil {
		panic(err)
	}
	defer db.Close()
	vi := db.VectorIndexes

	created := must(vi.CreateIndex(ctx, "demo", oc.CreateIndexOptions{Dimension: 2, Metric: "cosine"}))
	check(object(created["future_field"])["kept"] == true, "create keeps unknown fields")
	check(must(vi.DescribeIndex(ctx, "demo/one"))["name"] == "demo", "describe")
	var listed []any
	for index, err := range vi.AllIndexes(ctx, oc.ListIndexesOptions{Limit: 1}) {
		check(err == nil, "list indexes")
		listed = append(listed, index["name"])
	}
	check(fmt.Sprint(listed) == "[demo zeta]", "list indexes follows the cursor")

	out := must(vi.Upsert(ctx, "demo", "ns", oc.FromSlice([]map[string]any{
		{"id": "one", "values": []float64{1, 0}, "metadata": map[string]any{"tag": "test", "n": 1}},
		{"id": "two", "sparse_values": map[string]any{"indices": []int{3}, "values": []float64{0.5}}},
	}), oc.UpsertOptions{}))
	check(out.UpsertedCount == 2 && out.NamespaceCreated && out.Batches == 1, "upsert totals")

	partial := make([]map[string]any, 5)
	for i := range partial {
		partial[i] = map[string]any{"id": fmt.Sprintf("p%d", i)}
	}
	_, err = vi.Upsert(ctx, "demo-partial", "ns", oc.FromSlice(partial), oc.UpsertOptions{BatchSize: 2})
	// p0-p1 acknowledged; p2-p3 failed; p4 never sent.
	var batch *oc.BatchWriteError
	check(errors.As(err, &batch) && batch.Offset == 2 && batch.Count == 2 && batch.Outcome == "unknown", "partial upsert range")
	var cause *oc.Error
	check(errors.As(batch.Unwrap(), &cause) && cause.Code == "configuration_limit_exceeded" && cause.Reason == "exact_index_capacity", "partial upsert cause")

	res := must(vi.Search(ctx, "demo", "ns", oc.IndexSearchOptions{K: 2, Vector: []float64{1, 0}, Quality: "exact",
		FilterExpr: map[string]any{"tag": map[string]any{"$eq": "test"}}, IncludeMetadata: oc.Ptr(true)}))
	hits, _ := res["hits"].([]any)
	check(len(hits) == 2 && object(hits[0])["id"] == "one" && object(hits[1])["id"] == "two", "dense hits")
	metadata := object(object(hits[0])["metadata"])
	check(metadata["tag"] == "test" && metadata["n"] == json.Number("1") && res["future_field"] == true, "dense hit metadata")
	hybrid := must(vi.Search(ctx, "demo", "ns", oc.IndexSearchOptions{K: 2, Vector: []float64{1, 0},
		Lexical: map[string]any{"field": "body", "query": "hello"}}))
	check(hybrid["mode"] == "hybrid", "hybrid search")
	got := must(vi.Fetch(ctx, "demo", "ns", []string{"one", "missing"}, oc.FetchOptions{IncludeValues: oc.Ptr(true)}))
	values, _ := object(object(got["records"])["one"])["values"].([]any)
	check(len(values) == 2 && number(values[0]) == 1 && number(values[1]) == 0 && fmt.Sprint(got["missing"]) == "[missing]", "fetch")
	check(must(vi.Delete(ctx, "demo", "ns", []string{"one"}))["deleted"] == json.Number("1"), "delete by ids")
	job := must(vi.DeleteByFilter(ctx, "demo", "ns", oc.DeleteByFilterOptions{FilterExpr: map[string]any{"tag": map[string]any{"$in": []string{"a", "b"}}}}))
	jobID, _ := job["job_id"].(string)
	check(must(vi.GetJob(ctx, "demo", jobID))["state"] == "succeeded", "job status")
	spaces, _ := must(vi.ListNamespaces(ctx, "demo", oc.ListNamespacesOptions{}))["namespaces"].([]any)
	check(len(spaces) == 1 && object(spaces[0])["name"] == "ns", "list namespaces")
	check(must(vi.IndexStats(ctx, "demo"))["total_record_count"] == json.Number("1"), "statistics")
	check(must(vi.Count(ctx, "demo", "ns", oc.CountOptions{Filter: map[string]any{"tag": "test"}}))["count"] == json.Number("1"), "count")

	failures := []struct {
		call       func() error
		kind       string
		status     int
		code       string
		notEnabled bool
	}{
		{func() error { _, err := vi.DescribeIndex(ctx, "contract-unauthorized"); return err }, "authentication", 401, "unauthorized", false},
		{func() error { _, err := vi.DescribeIndex(ctx, "contract-key-scope"); return err }, "permission", 403, "key_scope_insufficient", false},
		{func() error { _, err := vi.DescribeIndex(ctx, "contract-missing"); return err }, "http", 404, "index_not_found", false},
		{func() error {
			_, err := vi.Search(ctx, "demo", "ns", oc.IndexSearchOptions{K: 1, Sparse: map[string]any{"indices": []int{3}, "values": []float64{0.5}}})
			return err
		}, "http", 409, "index_capability_missing", false},
		{func() error { _, err := vi.DescribeIndex(ctx, "contract-not-enabled"); return err }, "http", 501, "not_implemented", true},
		{func() error {
			_, err := vi.Search(ctx, "demo", "ns", oc.IndexSearchOptions{K: 3, Vector: []float64{1, 0}})
			return err
		}, "http", 503, "index_rebuilding", false},
		{func() error { _, err := vi.DescribeIndex(ctx, "contract-redirect"); return err }, "http", 307, "", false},
	}
	for _, f := range failures {
		err := f.call()
		var failure *oc.Error
		check(errors.As(err, &failure), fmt.Sprintf("expected failure %d", f.status))
		check(failure.Kind == f.kind && failure.Status == f.status && failure.Code == f.code, fmt.Sprintf("failure %d: %s %d %s", f.status, failure.Kind, failure.Status, failure.Code))
		check(failure.RequestID == requestID, "request id")
		check(f.code == "" || failure.Message == "server detail", "envelope message")
		check(errors.Is(err, oc.ErrRecordAPINotEnabled) == f.notEnabled, "record API not enabled")
	}
	fmt.Println("Go vector index consumer assertions passed")
}
