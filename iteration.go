package originchaindb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"regexp"
	"strings"
)

// BatchResult describes one acknowledged input range. Counts are server values.
type BatchResult struct {
	Offset   int64
	Count    int
	Inserted int
}

// BatchOptions bounds requests. Zero fields select 1000 rows and 1 MiB.
type BatchOptions struct {
	BatchSize int
	MaxBytes  int
}

// BatchWriteError identifies an unconfirmed range. Unknown outcomes must not be replayed blindly.
type BatchWriteError struct {
	Offset  int64
	Count   int
	Outcome string
	cause   error
}

func (e *BatchWriteError) Error() string {
	return fmt.Sprintf("batch at input offset %d failed (%s)", e.Offset, e.Outcome)
}
func (e *BatchWriteError) Unwrap() error { return e.cause }

// RowPage contains one keyset page. A nil NextCursor marks completion.
type RowPage struct {
	Rows       []map[string]any
	NextCursor *string
}

// PageOptions selects a page size and optional exclusive continuation string.
type PageOptions struct {
	PageSize int
	After    *string
}

func batchLimits(options BatchOptions) (int, int, error) {
	count, size := options.BatchSize, options.MaxBytes
	if count == 0 {
		count = 1000
	}
	if size == 0 {
		size = 1048576
	}
	if count < 1 || count > 10000 || size < 2 || size > 8388608 {
		return 0, 0, errors.New("batch bounds must be 1..10000 rows and 2..8388608 bytes")
	}
	return count, size, nil
}

// WriteBatches is a lazy sequential write iterator. Breaking the range stops input consumption and HTTP.
// At most one batch and one lookahead row are retained. No failed batch is replayed.
func (s *RowsService) WriteBatches(ctx context.Context, schema string, rows iter.Seq2[map[string]any, error], options BatchOptions) iter.Seq2[BatchResult, error] {
	return func(yield func(BatchResult, error) bool) {
		limit, maxBytes, err := batchLimits(options)
		if err != nil {
			yield(BatchResult{}, err)
			return
		}
		if _, err = path("rows", schema, "_batch"); err != nil {
			yield(BatchResult{}, err)
			return
		}
		if rows == nil {
			yield(BatchResult{}, errors.New("rows must not be nil"))
			return
		}
		batch := []map[string]any{}
		size := 2
		var offset int64
		fail := func(count int, outcome string, cause error) {
			yield(BatchResult{}, &BatchWriteError{offset, count, outcome, cause})
		}
		flush := func() bool {
			if err := ctx.Err(); err != nil {
				fail(len(batch), "not_sent", err)
				return false
			}
			value, err := s.PutBatch(ctx, schema, batch)
			if err != nil {
				fail(len(batch), "unknown", err)
				return false
			}
			number, ok := value["inserted"].(json.Number)
			inserted, parseErr := number.Int64()
			if !ok || parseErr != nil || inserted < 0 || inserted > int64(len(batch)) {
				fail(len(batch), "unknown", &Error{Kind: "protocol"})
				return false
			}
			result := BatchResult{offset, len(batch), int(inserted)}
			offset += int64(len(batch))
			batch = nil
			size = 2
			return yield(result, nil)
		}
		if err := ctx.Err(); err != nil {
			fail(0, "not_sent", err)
			return
		}
		for row, inputErr := range rows {
			if inputErr != nil {
				fail(len(batch), "not_sent", inputErr)
				return
			}
			if err := ctx.Err(); err != nil {
				fail(len(batch), "not_sent", err)
				return
			}
			if row == nil {
				fail(len(batch)+1, "not_sent", errors.New("row must be an object"))
				return
			}
			encoded, err := json.Marshal(row)
			if err != nil {
				fail(len(batch)+1, "not_sent", err)
				return
			}
			if len(encoded)+2 > maxBytes {
				fail(len(batch)+1, "not_sent", errors.New("row exceeds MaxBytes"))
				return
			}
			var copy map[string]any
			decoder := json.NewDecoder(bytes.NewReader(encoded))
			decoder.UseNumber()
			if err := decoder.Decode(&copy); err != nil {
				fail(len(batch)+1, "not_sent", err)
				return
			}
			if len(batch) > 0 && size+1+len(encoded) > maxBytes {
				if !flush() {
					return
				}
			}
			if len(batch) > 0 {
				size++
			}
			size += len(encoded)
			batch = append(batch, copy)
			if len(batch) == limit && !flush() {
				return
			}
		}
		if len(batch) > 0 {
			flush()
		}
	}
}

var simpleIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func keysetQuery(schema, key string, size int, after *string) (string, []any, error) {
	parts := strings.Split(schema, ".")
	if len(parts) > 2 || !simpleIdentifier.MatchString(key) {
		return "", nil, errors.New("use simple schema/table and key identifiers")
	}
	for i, p := range parts {
		if !simpleIdentifier.MatchString(p) {
			return "", nil, errors.New("use simple schema/table and key identifiers")
		}
		parts[i] = `"` + p + `"`
	}
	if size < 1 || size > 10000 {
		return "", nil, errors.New("PageSize must be from 1 to 10000")
	}
	where := ""
	params := []any{}
	if after != nil {
		where = ` WHERE "` + key + `" > $1`
		params = append(params, *after)
	}
	return fmt.Sprintf(`SELECT * FROM %s%s ORDER BY "%s" ASC LIMIT %d`, strings.Join(parts, "."), where, key, size), params, nil
}

// Pages performs keyset reads over a unique immutable string key. It is not a snapshot.
func (s *RowsService) Pages(ctx context.Context, schema, key string, options PageOptions) iter.Seq2[RowPage, error] {
	return func(yield func(RowPage, error) bool) {
		size := options.PageSize
		if size == 0 {
			size = 1000
		}
		after := options.After
		// Copy the caller's continuation before yielding control.
		if after != nil {
			value := *after
			after = &value
		}
		for {
			if err := ctx.Err(); err != nil {
				yield(RowPage{}, err)
				return
			}
			sql, params, err := keysetQuery(schema, key, size, after)
			if err != nil {
				yield(RowPage{}, err)
				return
			}
			result, err := s.client.SQL.Query(ctx, sql, params...)
			if err != nil {
				yield(RowPage{}, err)
				return
			}
			if len(result.Rows) > size {
				yield(RowPage{}, &Error{Kind: "protocol"})
				return
			}
			previous := after
			for _, row := range result.Rows {
				value, ok := row[key].(string)
				if !ok || (previous != nil && value <= *previous) {
					yield(RowPage{}, &Error{Kind: "protocol"})
					return
				}
				previous = &value
			}
			if len(result.Rows) == 0 {
				return
			}
			var next *string
			if len(result.Rows) == size {
				value := *previous
				next = &value
			}
			// Consumer changes to NextCursor must not change the internal continuation.
			value := *previous
			after = &value
			if !yield(RowPage{result.Rows, next}, nil) || next == nil {
				return
			}
		}
	}
}

// Scan yields rows from Pages with one page resident and no network prefetch.
func (s *RowsService) Scan(ctx context.Context, schema, key string, options PageOptions) iter.Seq2[map[string]any, error] {
	return func(yield func(map[string]any, error) bool) {
		for page, err := range s.Pages(ctx, schema, key, options) {
			if err != nil {
				yield(nil, err)
				return
			}
			for _, row := range page.Rows {
				if err := ctx.Err(); err != nil {
					yield(nil, err)
					return
				}
				if !yield(row, nil) {
					return
				}
			}
		}
	}
}
