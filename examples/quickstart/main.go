package main

import (
	"context"
	oc "github.com/originchaindb/originchaindb-go"
	"log"
)

func main() {
	db, err := oc.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	result, err := db.SQL.Run(context.Background(), "SELECT $1 AS value", 7)
	if err != nil {
		log.Fatal(err)
	}
	log.Println(result.Rows)
}
