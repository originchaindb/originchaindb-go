package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	oc "github.com/originchaindb/originchaindb-go"
)

func run() error {
	ctx := context.Background()
	db, err := oc.FromEnv()
	if err != nil {
		return err
	}
	defer db.Close()

	// section:setup
	schema := "sdk_examples.products"
	toml, err := os.ReadFile("examples/products.toml")
	if err != nil {
		return err
	}
	if _, err := db.Schemas.Register(ctx, string(toml)); err != nil {
		return err
	}
	// endsection

	// section:rows
	_, err = db.Rows.Put(ctx, schema, map[string]any{
		"id": "p2", "name": "Gadget", "description": "A red gadget",
	})
	if err != nil {
		return err
	}
	_, err = db.Rows.Put(ctx, schema, map[string]any{
		"id": "p1", "name": "Widget", "description": "A small blue widget", "related_to": "p2",
	})
	if err != nil {
		return err
	}
	product, err := db.Rows.Get(ctx, schema, "p1")
	if err != nil {
		return err
	}
	fmt.Println(product)
	// endsection

	// section:sql
	result, err := db.SQL.Run(ctx,
		"SELECT id, name FROM sdk_examples.products WHERE id = $1", "p1")
	if err != nil {
		return err
	}
	for _, row := range result.Rows {
		fmt.Println(row["id"], row["name"])
	}
	// endsection

	// section:vector
	embedding := []float64{0.1, 0.2, 0.3, 0.4}
	if _, err := db.Vector.Put(ctx, schema, "p1", embedding, map[string]any{"category": "tools"}); err != nil {
		return err
	}
	hits, err := db.Vector.Search(ctx, schema, embedding, oc.VectorSearchOptions{K: 5})
	if err != nil {
		return err
	}
	for _, hit := range hits {
		fmt.Println(hit.ID, hit.Score)
	}
	// endsection

	// section:fts
	if _, err := db.FTS.Index(ctx, schema, "description", "p1", "A small blue widget"); err != nil {
		return err
	}
	matches, err := db.FTS.Search(ctx, schema, "description", "blue widget", oc.FTSSearchOptions{K: 5})
	if err != nil {
		return err
	}
	for _, hit := range matches.Hits {
		fmt.Println(hit.DocID, hit.Score)
	}
	// endsection

	// section:graph
	neighbors, err := db.Graph.Neighbors(ctx, schema, "p1", "related")
	if err != nil {
		return err
	}
	fmt.Println(neighbors)
	// endsection
	return nil
}

func main() {
	if err := run(); err != nil {
		var failure *oc.Error
		if errors.As(err, &failure) {
			log.Printf("Request failed: kind=%s status=%d request_id=%s", failure.Kind, failure.Status, failure.RequestID)
		}
		log.Fatal(err)
	}
}
