// Private integration worker. Credentials arrive over stdin and are never logged.
package main

import (
	"context"
	"encoding/json"
	"errors"
	db "github.com/originchaindb/originchaindb-go"
	"io"
	"os"
)

func check(value bool) {
	if !value {
		panic("engine compatibility assertion failed")
	}
}
func main() {
	current := "unset"
	calls := 0
	ctx := context.Background()
	client, err := db.NewClient(db.Config{URL: os.Getenv("OC_URL"), TokenProvider: func(context.Context) (string, error) { calls++; return current, nil }})
	if err != nil {
		panic(err)
	}
	defer client.Close()
	input, output := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var cmd struct {
			Action   string `json:"action"`
			Token    string `json:"token"`
			SQL      string `json:"sql"`
			Schema   string `json:"schema"`
			Params   []any  `json:"params"`
			Status   int    `json:"status"`
			Rows     int    `json:"rows"`
			Requests int    `json:"requests"`
		}
		if err := input.Decode(&cmd); err == io.EOF {
			return
		} else if err != nil {
			panic(err)
		}
		current = cmd.Token
		before, status := calls, 200
		switch cmd.Action {
		case "query":
			result, e := client.SQL.Run(ctx, cmd.SQL, cmd.Params...)
			if e != nil {
				var failure *db.Error
				check(errors.As(e, &failure))
				status = failure.Status
				check(cmd.Status != 200 && status == cmd.Status)
			} else {
				check(cmd.Status == 200 && len(result.Rows) == cmd.Rows)
			}
		case "bulk":
			source := func(yield func(map[string]any, error) bool) {
				for i, id := range "abcde" {
					if !yield(map[string]any{"id": string(id), "value": i}, nil) {
						return
					}
				}
			}
			count := 0
			for b, e := range client.Rows.WriteBatches(ctx, cmd.Schema, source, db.BatchOptions{BatchSize: 2}) {
				if e != nil {
					panic(e)
				}
				check(b.Offset == int64(count*2))
				want := 2
				if count == 2 {
					want = 1
				}
				check(b.Count == want && b.Inserted == want)
				count++
			}
			check(count == 3)
		case "scan":
			ids := ""
			for row, e := range client.Rows.Scan(ctx, cmd.Schema, "id", db.PageOptions{PageSize: 2}) {
				if e != nil {
					panic(e)
				}
				ids += row["id"].(string)
			}
			check(ids == "abcde")
		default:
			panic("unknown compatibility action")
		}
		check(calls-before == cmd.Requests)
		if err := output.Encode(map[string]any{"ok": true, "status": status, "requests": calls - before}); err != nil {
			panic(err)
		}
	}
}
