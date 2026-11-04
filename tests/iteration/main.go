package main

import (
	"context"
	"errors"
	"fmt"
	db "github.com/originchaindb/originchaindb-go"
	"iter"
	"os"
	"strings"
)

func check(ok bool) {
	if !ok {
		panic("iteration assertion failed")
	}
}
func source(consumed *int) iter.Seq2[map[string]any, error] {
	return func(yield func(map[string]any, error) bool) {
		row := map[string]any{}
		for _, k := range "abcde" {
			*consumed++
			row["id"] = string(k)
			if !yield(row, nil) {
				return
			}
		}
	}
}
func main() {
	client, err := db.New(os.Getenv("OC_URL"), os.Getenv("OC_TOKEN"))
	if err != nil {
		panic(err)
	}
	defer client.Close()
	ctx := context.Background()
	n := 0
	batches := client.Rows.WriteBatches(ctx, "bulk.ok", source(&n), db.BatchOptions{BatchSize: 2})
	check(n == 0)
	count := 0
	for b, e := range batches {
		if e != nil {
			panic(e)
		}
		check(b.Offset == int64(count*2))
		if count < 2 {
			check(b.Count == 2)
		} else {
			check(b.Count == 1)
		}
		check(b.Inserted == b.Count)
		count++
		check(n == count*2 || n == 5)
	}
	check(count == 3)
	for _, item := range []struct {
		name     string
		offset   int64
		consumed int
	}{{"fail", 2, 4}, {"lost", 0, 2}, {"unknown", 0, 2}} {
		n = 0
		failed := false
		for _, e := range client.Rows.WriteBatches(ctx, "bulk."+item.name, source(&n), db.BatchOptions{BatchSize: 2}) {
			if e != nil {
				var b *db.BatchWriteError
				check(errors.As(e, &b))
				check(b.Offset == item.offset && b.Count == 2 && b.Outcome == "unknown" && errors.Unwrap(b) != nil)
				failed = true
			}
		}
		check(failed && n == item.consumed)
	}
	bytes := func(yield func(map[string]any, error) bool) {
		for _, k := range "abc" {
			if !yield(map[string]any{"id": string(k), "value": strings.Repeat("x", 20)}, nil) {
				return
			}
		}
	}
	count = 0
	for b, e := range client.Rows.WriteBatches(ctx, "bulk.bytes", bytes, db.BatchOptions{MaxBytes: 64}) {
		if e != nil {
			panic(e)
		}
		check(b.Count == 1)
		count++
	}
	check(count == 3)
	n = 0
	for _, e := range client.Rows.WriteBatches(ctx, "bulk.stop", source(&n), db.BatchOptions{BatchSize: 2}) {
		check(e == nil)
		break
	}
	check(n == 2)
	empty := func(func(map[string]any, error) bool) {}
	for range client.Rows.WriteBatches(ctx, "bulk.never", empty, db.BatchOptions{}) {
		panic("empty input wrote")
	}
	for _, e := range client.Rows.WriteBatches(ctx, "bulk.never", source(&n), db.BatchOptions{MaxBytes: 4}) {
		var b *db.BatchWriteError
		check(errors.As(e, &b) && b.Outcome == "not_sent" && b.Offset == 0)
	}
	for _, e := range client.Rows.WriteBatches(ctx, "bulk.never", empty, db.BatchOptions{BatchSize: -1}) {
		check(e != nil)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, e := range client.Rows.WriteBatches(canceled, "bulk.never", source(&n), db.BatchOptions{}) {
		check(errors.Is(e, context.Canceled))
	}
	ids := ""
	for row, e := range client.Rows.Scan(ctx, "scan.items", "id", db.PageOptions{PageSize: 2}) {
		if e != nil {
			panic(e)
		}
		ids += row["id"].(string)
	}
	check(ids == "abcde")
	for row, e := range client.Rows.Scan(ctx, "scan.stop", "id", db.PageOptions{PageSize: 2}) {
		check(e == nil && row["id"] == "a")
		break
	}
	after := "b"
	count = 0
	ids = ""
	for page, e := range client.Rows.Pages(ctx, "scan.resume", "id", db.PageOptions{PageSize: 2, After: &after}) {
		if e != nil {
			panic(e)
		}
		if count == 0 {
			check(page.NextCursor != nil && *page.NextCursor == "d")
		} else {
			check(page.NextCursor == nil)
		}
		for _, row := range page.Rows {
			ids += row["id"].(string)
		}
		count++
	}
	check(ids == "cde" && count == 2)
	for _, name := range []string{"bad", "numeric", "oversize"} {
		failed := false
		for _, e := range client.Rows.Pages(ctx, "scan."+name, "id", db.PageOptions{PageSize: 2}) {
			check(e != nil)
			failed = true
		}
		check(failed)
	}
	for range client.Rows.Scan(ctx, "scan.empty", "id", db.PageOptions{PageSize: 2}) {
		panic("empty page yielded")
	}
	for _, e := range client.Rows.Scan(ctx, "scan.items;DELETE", "id", db.PageOptions{}) {
		check(e != nil)
	}
	fmt.Println("Go iteration assertions passed")
}
