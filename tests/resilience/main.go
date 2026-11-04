package main

import (
	"context"
	"errors"
	"fmt"
	db "github.com/originchaindb/originchaindb-go"
	"os"
	"strings"
	"time"
)

func check(ok bool) {
	if !ok {
		panic("resilience assertion failed")
	}
}
func failure(client *db.Client, name string) *db.Error {
	_, err := client.SQL.Run(context.Background(), "fault:"+name)
	var result *db.Error
	check(errors.As(err, &result))
	return result
}
func main() {
	url, token := os.Getenv("OC_URL"), os.Getenv("OC_TOKEN")
	client, err := db.New(url, token)
	if err != nil {
		panic(err)
	}
	defer client.Close()
	for _, item := range []struct {
		name   string
		status int
	}{{"expired", 401}, {"revoked", 401}, {"forbidden", 403}, {"limited", 429}} {
		e := failure(client, item.name)
		check(e.Status == item.status && e.RequestID == "84f92822-5837-432e-981a-7b24ad65e16c")
		if item.name == "limited" {
			check(e.RetryAfter == "2")
		}
	}
	failure(client, "interrupted")
	slow, err := db.New(url, token, db.WithTimeout(150*time.Millisecond))
	if err != nil {
		panic(err)
	}
	defer slow.Close()
	check(failure(slow, "timeout").Kind == "timeout")
	calls := 0
	events := []db.RequestEvent{}
	rotating, err := db.NewClient(db.Config{URL: url, TokenProvider: func(context.Context) (string, error) { calls++; return fmt.Sprintf("rotate-%d", calls), nil }, OnRequest: func(e db.RequestEvent) { events = append(events, e) }})
	if err != nil {
		panic(err)
	}
	defer rotating.Close()
	check(failure(rotating, "rotate").Status == 401 && calls == 1)
	result, err := rotating.SQL.Run(context.Background(), "fault:rotate")
	if err != nil {
		panic(err)
	}
	check(calls == 2 && len(events) == 2 && events[0].ErrorKind == "authentication" && events[1].ErrorKind == "")
	check(events[1].Operation == "sql" && events[1].Duration >= 0)
	check(!strings.Contains(fmt.Sprint(events), "rotate-") && !strings.Contains(fmt.Sprint(events), url))
	type Product struct {
		ID     int64  `json:"id"`
		Amount string `json:"amount"`
	}
	products, err := db.DecodeRows[Product](result)
	if err != nil {
		panic(err)
	}
	check(products[0].ID == 9007199254740993 && products[0].Amount == "1.00000000000000001")
	type Bad struct {
		ID int8 `json:"id"`
	}
	_, err = db.DecodeRows[Bad](result)
	var decode *db.RowDecodeError
	check(errors.As(err, &decode) && decode.Row == 0)
	invalid, err := db.NewClient(db.Config{URL: url, TokenProvider: func(context.Context) (string, error) { return "bad\nheader", nil }})
	if err != nil {
		panic(err)
	}
	defer invalid.Close()
	check(failure(invalid, "invalid-provider").Kind == "credential")
	observed, err := db.NewClient(db.Config{URL: url, Token: token, OnRequest: func(db.RequestEvent) { panic("observer") }})
	if err != nil {
		panic(err)
	}
	defer observed.Close()
	result, err = observed.SQL.Run(context.Background(), "fault:observer")
	if err != nil {
		panic(err)
	}
	check(len(result.Rows) == 1)
	fmt.Println("Go resilience assertions passed")
}
