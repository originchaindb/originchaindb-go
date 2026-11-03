package originchaindb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// SQLResult preserves statement results and unknown server fields. JSON numbers
// in Rows and Raw are json.Number; use Int64 or a decimal library as appropriate.
type SQLResult struct {
	Kind           string
	Rows           []map[string]any
	Columns        []string
	ColumnTypes    map[string]string
	RowsAffected   *int64
	RowsBuffered   *int64
	Results        []*SQLResult
	StoppedOnError bool
	Raw            map[string]any
}

type SQLService struct{ client *Client }

// Run submits one statement or a batch. A batch is not automatically atomic.
// Failed batches return both a result and an *Error containing Partial.
func (s *SQLService) Run(ctx context.Context, statement string, params ...any) (*SQLResult, error) {
	if strings.TrimSpace(statement) == "" {
		return nil, errors.New("SQL statement must not be empty")
	}
	if params == nil {
		params = []any{}
	}
	value, err := s.client.request(ctx, http.MethodPost, "sql", nil, map[string]any{"sql": statement, "params": params}, nil)
	if err != nil {
		return nil, err
	}
	result, err := decodeSQL(value)
	if err != nil {
		return nil, &Error{Kind: "protocol", cause: err}
	}
	if failed(result) {
		return result, &Error{Kind: "batch", Status: 200, Partial: result}
	}
	return result, nil
}

// Query requires a SELECT response. Use Run for mixed or mutation results.
func (s *SQLService) Query(ctx context.Context, statement string, params ...any) (*SQLResult, error) {
	r, err := s.Run(ctx, statement, params...)
	if err != nil {
		return r, err
	}
	if r.Kind != "select" {
		return r, &Error{Kind: "protocol", Status: 200}
	}
	return r, nil
}

// Execute returns counts, RETURNING rows and batch details for any statement.
func (s *SQLService) Execute(ctx context.Context, statement string, params ...any) (*SQLResult, error) {
	return s.Run(ctx, statement, params...)
}

func failed(r *SQLResult) bool {
	if r.StoppedOnError || r.Kind == "error" {
		return true
	}
	if _, ok := r.Raw["error"]; ok {
		return true
	}
	for _, child := range r.Results {
		if failed(child) {
			return true
		}
	}
	return false
}

func decodeSQL(value any) (*SQLResult, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("SQL result must be an object")
	}
	kind, ok := obj["kind"].(string)
	if !ok {
		if _, hasError := obj["error"]; hasError {
			kind = "error"
		} else {
			return nil, errors.New("SQL result must have a kind")
		}
	}
	r := &SQLResult{Kind: kind, Rows: []map[string]any{}, Results: []*SQLResult{}, Raw: obj}
	if raw, exists := obj["rows"]; exists {
		items, ok := raw.([]any)
		if !ok {
			return nil, errors.New("rows must be an array")
		}
		for _, item := range items {
			row, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("row must be an object")
			}
			r.Rows = append(r.Rows, row)
		}
	}
	columns, exists := obj["columns"]
	if !exists {
		columns, exists = obj["returning"]
	}
	if exists {
		items, ok := columns.([]any)
		if !ok {
			return nil, errors.New("columns must be an array")
		}
		for _, item := range items {
			column, ok := item.(string)
			if !ok {
				return nil, errors.New("column must be a string")
			}
			r.Columns = append(r.Columns, column)
		}
	}
	if raw, exists := obj["column_types"]; exists {
		types, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("column_types must be an object")
		}
		r.ColumnTypes = map[string]string{}
		for key, v := range types {
			t, ok := v.(string)
			if !ok {
				return nil, errors.New("column type must be a string")
			}
			r.ColumnTypes[key] = t
		}
	}
	count := func(key string) (*int64, error) {
		value, exists := obj[key]
		if !exists || value == nil {
			return nil, nil
		}
		number, ok := value.(json.Number)
		if !ok {
			return nil, fmt.Errorf("%s must be an integer", key)
		}
		n, err := number.Int64()
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid %s", key)
		}
		return &n, nil
	}
	var err error
	r.RowsAffected, err = count("rows_affected")
	if err != nil {
		return nil, err
	}
	if r.RowsAffected == nil && kind == "insert" {
		r.RowsAffected, err = count("inserted")
		if err != nil {
			return nil, err
		}
	}
	if r.RowsAffected == nil && kind == "select" {
		zero := int64(0)
		r.RowsAffected = &zero
	}
	r.RowsBuffered, err = count("rows_buffered")
	if err != nil {
		return nil, err
	}
	if raw, exists := obj["stopped_on_error"]; exists {
		r.StoppedOnError, ok = raw.(bool)
		if !ok {
			return nil, errors.New("stopped_on_error must be a boolean")
		}
	}
	if raw, exists := obj["results"]; exists {
		children, ok := raw.([]any)
		if !ok {
			return nil, errors.New("results must be an array")
		}
		for _, child := range children {
			decoded, err := decodeSQL(child)
			if err != nil {
				return nil, err
			}
			r.Results = append(r.Results, decoded)
		}
	}
	return r, nil
}
