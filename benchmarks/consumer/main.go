package main

import (
	"context"
	"encoding/json"
	"fmt"
	db "github.com/originchaindb/originchaindb-go"
	"os"
	"sync"
	"time"
)

func main() {
	client, err := db.New(os.Getenv("OC_URL"), os.Getenv("OC_TOKEN"))
	if err != nil {
		panic(err)
	}
	defer client.Close()
	report := map[string]any{}
	query := func(mode string) float64 {
		start := time.Now()
		result, err := client.SQL.Query(context.Background(), "bench:"+mode)
		if err != nil {
			panic(err)
		}
		expected := 1
		if mode == "large" {
			expected = 2048
		}
		if len(result.Rows) != expected {
			panic("unexpected result size")
		}
		return float64(time.Since(start)) / float64(time.Millisecond)
	}
	for i := 0; i < 10; i++ {
		query("warm")
	}
	for _, item := range []struct {
		mode  string
		count int
	}{{"sequential", 100}, {"concurrent", 64}, {"large", 10}} {
		values := make([]float64, item.count)
		start := time.Now()
		if item.mode == "concurrent" {
			var workers sync.WaitGroup
			for worker := 0; worker < 8; worker++ {
				workers.Add(1)
				go func(worker int) {
					defer workers.Done()
					for i := worker; i < item.count; i += 8 {
						values[i] = query(item.mode)
					}
				}(worker)
			}
			workers.Wait()
		} else {
			for i := range values {
				values[i] = query(item.mode)
			}
		}
		report[item.mode+"_ms"] = values
		report[item.mode+"_rps"] = float64(item.count) / time.Since(start).Seconds()
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		panic(err)
	}
	fmt.Println("OC_BENCHMARK " + string(encoded))
}
