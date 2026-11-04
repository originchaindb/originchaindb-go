package originchaindb

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// RowDecodeError identifies the zero-based row that could not be mapped.
// Error omits the value. Unwrap exposes the native decoder error on request.
type RowDecodeError struct {
	Row   int
	cause error
}

func (e *RowDecodeError) Error() string { return fmt.Sprintf("cannot decode SQL row %d", e.Row) }
func (e *RowDecodeError) Unwrap() error { return e.cause }

// DecodeRows maps a result into application structs using encoding/json tags.
// Raw rows are unchanged. Interface fields retain json.Number. Unknown fields
// follow encoding/json's usual behavior; application validation remains explicit.
// No partial list is returned when any row fails to decode.
func DecodeRows[T any](result *SQLResult) ([]T, error) {
	if result == nil {
		return nil, fmt.Errorf("SQL result must not be nil")
	}
	rows := make([]T, len(result.Rows))
	for i, row := range result.Rows {
		data, err := json.Marshal(row)
		if err != nil {
			return nil, &RowDecodeError{Row: i, cause: err}
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err = decoder.Decode(&rows[i]); err != nil {
			return nil, &RowDecodeError{Row: i, cause: err}
		}
	}
	return rows, nil
}
