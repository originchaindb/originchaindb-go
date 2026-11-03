package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	oc "github.com/originchaindb/originchaindb-go"
)

func check(ok bool) {
	if !ok {
		panic("contract assertion failed")
	}
}
func must(_ any, err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	ctx := context.Background()
	c, err := oc.FromEnv()
	if err != nil {
		panic(err)
	}
	defer c.Close()
	r, err := c.SQL.Run(ctx, "contract:select", 7, "quote' / 雪", nil)
	if err != nil {
		panic(err)
	}
	check(r.Rows[0]["id"].(json.Number).String() == "9007199254740993")
	check(r.Rows[0]["amount"] == "1.00000000000000001" && r.Raw["future_field"] != nil)
	r, err = c.SQL.Run(ctx, "contract:update")
	if err != nil {
		panic(err)
	}
	check(*r.RowsAffected == 2 && *r.RowsBuffered == 3 && len(r.Rows) == 1 && r.Columns[0] == "id")
	r, err = c.SQL.Run(ctx, "contract:unknown")
	if err != nil {
		panic(err)
	}
	check(r.Kind == "future_statement" && r.RowsAffected == nil)
	for _, name := range []string{"batch", "unavailable", "unauthorized", "redirect", "malformed", "invalid-rows"} {
		_, err = c.SQL.Run(ctx, "contract:"+name)
		var failure *oc.Error
		check(errors.As(err, &failure))
		if name == "batch" {
			check(failure.Partial != nil && *failure.Partial.Results[0].RowsAffected == 1)
		}
		if name == "unauthorized" {
			check(failure.Status == 401 && failure.RequestID == "84f92822-5837-432e-981a-7b24ad65e16c")
		}
		if name == "unavailable" {
			check(failure.Status == 503 && failure.RetryAfter == "0")
		}
	}
	must(c.Rows.Get(ctx, "demo/items", "a b?/%"))
	must(c.Rows.Put(ctx, "demo.items", map[string]any{"id": "one", "nullable": nil}))
	must(c.Rows.PutBatch(ctx, "demo.items", []map[string]any{{"id": "one"}, {"id": "two"}}))
	must(c.Schemas.List(ctx))
	must(c.Schemas.Register(ctx, "[schema]\nname=\"demo.items\"\n"))
	must(c.Vector.Put(ctx, "demo.items", "one", []float64{1, 0}, map[string]any{"tag": "test"}))
	vectors, err := c.Vector.Search(ctx, "demo.items", []float64{1, 0}, oc.VectorSearchOptions{})
	if err != nil {
		panic(err)
	}
	check(vectors[0].ID == "one" && vectors[1].ID == "two")
	must(c.Vector.Delete(ctx, "demo.items", "one"))
	must(c.FTS.Index(ctx, "demo.items", "body", "one", "hello world"))
	ranked, err := c.FTS.Search(ctx, "demo.items", "body", "hello", oc.FTSSearchOptions{})
	if err != nil {
		panic(err)
	}
	check(ranked.Hits[0].DocID == "one")
	phrase, err := c.FTS.Search(ctx, "demo.items", "body", "phrase", oc.FTSSearchOptions{Mode: "phrase"})
	if err != nil {
		panic(err)
	}
	check(phrase.Hits[0].DocID == "two" && phrase.Hits[0].Score == 0)
	enriched, err := c.FTS.Search(ctx, "demo.items", "body", "highlight", oc.FTSSearchOptions{Highlight: true, Facets: []string{"tag"}})
	if err != nil {
		panic(err)
	}
	check(enriched.Facets != nil && enriched.Hits[0].Highlights != nil)
	must(c.Graph.Query(ctx, "MATCH (n) RETURN $name", map[string]any{"name": "one"}))
	must(c.Graph.Neighbors(ctx, "demo.items", "one", "related"))
	must(c.Graph.BFS(ctx, "demo.items", "one", "related", 5))
	if fmt.Sprint(c) == os.Getenv("OC_TOKEN") {
		panic("credential disclosure")
	}
	fmt.Println("Go consumer assertions passed")
}
