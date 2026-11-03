package originchaindb

import "encoding/json"

// VectorHit normalizes the server's id/vec_id spelling without losing metadata.
type VectorHit struct {
	ID       string
	Score    float64
	Metadata map[string]any
	Raw      map[string]any
}

func decodeVector(value any) ([]VectorHit, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, &Error{Kind: "protocol"}
	}
	hits := make([]VectorHit, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, &Error{Kind: "protocol"}
		}
		id, ok := object["id"].(string)
		if !ok {
			id, ok = object["vec_id"].(string)
		}
		if !ok {
			return nil, &Error{Kind: "protocol"}
		}
		number, ok := object["score"].(json.Number)
		if !ok {
			return nil, &Error{Kind: "protocol"}
		}
		score, err := number.Float64()
		if err != nil {
			return nil, &Error{Kind: "protocol"}
		}
		metadata, _ := object["metadata"].(map[string]any)
		hits = append(hits, VectorHit{ID: id, Score: score, Metadata: metadata, Raw: object})
	}
	return hits, nil
}

func objectResult(value any, err error) (map[string]any, error) {
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, &Error{Kind: "protocol"}
	}
	return object, nil
}
