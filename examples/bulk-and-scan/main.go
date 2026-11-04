// Run against a development database; writes sdk_iteration.items.
package main

import (
	"context"
	"fmt"
	oc "github.com/originchaindb/originchaindb-go"
	"iter"
	"log"
	"os"
)

func input() iter.Seq2[map[string]any, error] {
	return func(yield func(map[string]any, error) bool) {
		for _, key := range "abcde" {
			if !yield(map[string]any{"id": string(key), "name": "Item " + string(key)}, nil) {
				return
			}
		}
	}
}
func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := oc.FromEnv()
	if err != nil {
		return err
	}
	defer db.Close()
	schema := "sdk_iteration.items"
	toml, err := os.ReadFile("examples/items.toml")
	if err != nil {
		return err
	}
	if _, err = db.Schemas.Register(ctx, string(toml)); err != nil {
		return err
	}
	rows := input()
	// section:bulk
	for batch, err := range db.Rows.WriteBatches(ctx, schema, rows, oc.BatchOptions{BatchSize: 2}) {
		if err != nil {
			return err
		}
		fmt.Println(batch.Offset, batch.Count, batch.Inserted)
	}
	// endsection
	// section:scan
	for row, err := range db.Rows.Scan(ctx, schema, "id", oc.PageOptions{PageSize: 2}) {
		if err != nil {
			return err
		}
		fmt.Println(row["id"], row["name"])
	}
	// endsection
	// Save a checkpoint only after successfully processing the complete page.
	after := "b"
	for page, err := range db.Rows.Pages(ctx, schema, "id", oc.PageOptions{PageSize: 2, After: &after}) {
		if err != nil {
			return err
		}
		for _, row := range page.Rows {
			fmt.Println("resumed", row["id"])
		}
		if page.NextCursor == nil {
			fmt.Println("complete")
		} else {
			fmt.Println("next cursor", *page.NextCursor)
		}
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
