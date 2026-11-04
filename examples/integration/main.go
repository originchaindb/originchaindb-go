package main

import (
	"context"
	"fmt"
	db "github.com/originchaindb/originchaindb-go"
	"net/http"
	"os"
)

type Product struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func main() {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Configure transport.Proxy or TLSClientConfig with your application's trust roots.
	client, err := db.NewClient(db.Config{URL: os.Getenv("OC_URL"),
		TokenProvider: func(ctx context.Context) (string, error) {
			// Replace with a current credential cache; honor ctx for remote lookups.
			return os.Getenv("OC_TOKEN"), ctx.Err()
		}, OnRequest: func(event db.RequestEvent) { fmt.Println(event.Operation, event.Status, event.Duration) },
	}, db.WithTransport(transport))
	if err != nil {
		panic(err)
	}
	defer client.Close()
	result, err := client.SQL.Run(context.Background(), "SELECT $1 AS id, $2 AS name", "p1", "Widget")
	if err != nil {
		panic(err)
	}
	products, err := db.DecodeRows[Product](result)
	if err != nil {
		panic(err)
	}
	fmt.Println(products)
}
