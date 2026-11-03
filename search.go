package originchaindb

import (
	"encoding/json"
	"errors"
)

// FTSHit has the same shape for ranked, boolean, and phrase searches.
// Unranked hits have Score zero. Raw retains additional server fields.
type FTSHit struct {
	DocID      string
	Score      float64
	Highlights any
	Raw        map[string]any
}

type FTSResult struct {
	Hits   []FTSHit
	Facets any
	Raw    any
}

func decodeFTS(value any) (*FTSResult, error) {
	r := &FTSResult{Hits: []FTSHit{}, Raw: value}
	hits := value
	if envelope, ok := value.(map[string]any); ok {
		hits = envelope["hits"]
		r.Facets = envelope["facets"]
	}
	list, ok := hits.([]any)
	if !ok {
		return nil, &Error{Kind: "protocol", cause: errors.New("FTS hits must be an array")}
	}
	for _, item := range list {
		if id, ok := item.(string); ok {
			r.Hits = append(r.Hits, FTSHit{DocID: id})
			continue
		}
		object, ok := item.(map[string]any)
		if !ok {
			return nil, &Error{Kind: "protocol"}
		}
		id, ok := object["doc_id"].(string)
		if !ok {
			return nil, &Error{Kind: "protocol"}
		}
		var score float64
		if v, exists := object["score"]; exists {
			number, ok := v.(json.Number)
			if !ok {
				return nil, &Error{Kind: "protocol"}
			}
			var err error
			score, err = number.Float64()
			if err != nil {
				return nil, &Error{Kind: "protocol"}
			}
		}
		r.Hits = append(r.Hits, FTSHit{DocID: id, Score: score, Highlights: object["highlights"], Raw: object})
	}
	return r, nil
}
