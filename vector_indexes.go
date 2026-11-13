package originchaindb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
)

const (
	// VectorUpsertMaxRecords is the engine's cap on records in one upsert request.
	VectorUpsertMaxRecords = 1000
	// VectorUpsertMaxBodyBytes is the engine's bound on one upsert request body, in bytes.
	VectorUpsertMaxBodyBytes = 2_000_000
	// VectorDefaultNamespace names the default namespace.
	VectorDefaultNamespace = "__default__"
)

// VectorIndexesService holds the record operations on vector indexes.
//
// An index holds namespaces, and a namespace holds records. A record has an
// "id" and any of a dense vector ("values"), a sparse vector ("sparse_values"),
// flat "metadata" and declared "text" fields. Every method takes the index by
// its name or its index_id. Record calls always name a namespace;
// VectorDefaultNamespace is the default one.
//
// The routes are $OC_URL/vector-indexes/... . They are separate from the
// per-table Vector service ($OC_URL/vector/<table>/...), which keeps working
// unchanged; the two never share data.
//
// Results are the server's JSON objects, unchanged: numbers are json.Number and
// fields this SDK does not know are kept. Errors are *Error; branch on Code
// plus Reason, never on Status alone. A database where these operations are
// not enabled answers every call with an error matching ErrRecordAPINotEnabled.
//
// Reads (describe, list, fetch, count, statistics, search and job status) are
// sent again after a retryable failure when Config.MaxRetries allows it.
// Writes are never sent twice.
type VectorIndexesService struct{ client *Client }

// Ptr returns a pointer to value, for optional fields such as
// FetchOptions.IncludeMetadata or CreateNamespaceOptions.DeclaredCapacity.
func Ptr[T any](value T) *T { return &value }

// ── Indexes ───────────────────────────────────────────────────────────

// VectorTextField declares one text field of an index. Analyzer is optional.
type VectorTextField struct {
	Name     string `json:"name"`
	Analyzer string `json:"analyzer,omitempty"`
}

// CreateIndexOptions describes a new index. At least one of Dimension, Sparse
// or TextFields is required. Fields left at their zero value are not sent;
// a non-nil empty slice or map is sent empty.
type CreateIndexOptions struct {
	// Dimension is the length of the index's dense vectors.
	Dimension int
	// Metric is "cosine", "dot", "l2" or "manhattan" and goes with Dimension.
	Metric string
	// Sparse declares sparse vectors.
	Sparse bool
	// TextFields declares the fields keyword (BM25) search runs over.
	TextFields       []VectorTextField
	VisibilityFields []string
	// IdentifierProfile is "unicode" (the default) or "ascii".
	IdentifierProfile string
	// DeletionProtection is "enabled" or "disabled".
	DeletionProtection string
	Tags               map[string]string
}

// CreateIndex creates an index and returns it. A name that is already taken
// returns Code "index_already_exists" (409). Creation is not sent twice, so
// after a lost answer, describe the index.
func (s *VectorIndexesService) CreateIndex(ctx context.Context, name string, options CreateIndexOptions) (map[string]any, error) {
	body := jsonObject{}.set("name", name).
		setIf(options.Dimension != 0, "dimension", options.Dimension).
		setIf(options.Metric != "", "metric", options.Metric).
		setIf(options.Sparse, "sparse", true).
		setIf(options.TextFields != nil, "text_fields", options.TextFields).
		setIf(options.VisibilityFields != nil, "visibility_fields", options.VisibilityFields).
		setIf(options.IdentifierProfile != "", "identifier_profile", options.IdentifierProfile).
		setIf(options.DeletionProtection != "", "deletion_protection", options.DeletionProtection).
		setIf(options.Tags != nil, "tags", options.Tags)
	return s.do(ctx, http.MethodPost, "vector-indexes", body, false)
}

// DescribeIndex returns one index by name or index_id.
func (s *VectorIndexesService) DescribeIndex(ctx context.Context, index string) (map[string]any, error) {
	p, err := indexPath(index, "")
	if err != nil {
		return nil, err
	}
	return s.do(ctx, http.MethodGet, p, nil, true)
}

// ListIndexesOptions selects a page of indexes. Send at most one of Name (an
// exact name) and Prefix. Cursor is a previous page's next_cursor.
type ListIndexesOptions struct {
	Name   string
	Prefix string
	Limit  int
	Cursor string
}

// ListIndexes returns one page of live indexes in ascending name order:
// {"indexes": [...], "next_cursor": ...}. next_cursor is absent on the last page.
func (s *VectorIndexesService) ListIndexes(ctx context.Context, options ListIndexesOptions) (map[string]any, error) {
	body := jsonObject{}.
		setIf(options.Name != "", "name", options.Name).
		setIf(options.Prefix != "", "prefix", options.Prefix).
		setIf(options.Limit != 0, "limit", options.Limit).
		setIf(options.Cursor != "", "cursor", options.Cursor)
	return s.do(ctx, http.MethodPost, "vector-indexes/list", body, true)
}

// AllIndexes yields every live index from ListIndexes pages, starting at
// options.Cursor. One page is held at a time and nothing is prefetched.
func (s *VectorIndexesService) AllIndexes(ctx context.Context, options ListIndexesOptions) iter.Seq2[map[string]any, error] {
	return pages(ctx, options.Cursor, func(cursor string) (map[string]any, []byte, error) {
		page := options
		page.Cursor = cursor
		answer, err := s.ListIndexes(ctx, page)
		return answer, nil, err
	}, objectItems("indexes"))
}

// ConfigureIndexOptions changes an index. Set at least one field. A tag set
// to "" is removed.
type ConfigureIndexOptions struct {
	// DeletionProtection is "enabled" or "disabled".
	DeletionProtection string
	Tags               map[string]string
}

// ConfigureIndex changes deletion protection or tags and returns the index.
func (s *VectorIndexesService) ConfigureIndex(ctx context.Context, index string, options ConfigureIndexOptions) (map[string]any, error) {
	p, err := indexPath(index, "")
	if err != nil {
		return nil, err
	}
	if options.DeletionProtection == "" && options.Tags == nil {
		return nil, errors.New("ConfigureIndex needs DeletionProtection or Tags")
	}
	body := jsonObject{}.
		setIf(options.DeletionProtection != "", "deletion_protection", options.DeletionProtection).
		setIf(options.Tags != nil, "tags", options.Tags)
	return s.do(ctx, http.MethodPatch, p, body, false)
}

// DeleteIndex starts deleting an index and every record in it, and returns the
// index with state "deleting". An index with deletion protection returns Code
// "deletion_protection_enabled".
func (s *VectorIndexesService) DeleteIndex(ctx context.Context, index string) (map[string]any, error) {
	p, err := indexPath(index, "")
	if err != nil {
		return nil, err
	}
	return s.do(ctx, http.MethodDelete, p, nil, false)
}

// IndexStats returns record counts per namespace and in total:
// {"namespaces": [...], "total_record_count", "truncated", ...}. At most 1,000
// namespaces are listed; page the rest with ListNamespaces.
func (s *VectorIndexesService) IndexStats(ctx context.Context, index string) (map[string]any, error) {
	p, err := indexPath(index, "/stats")
	if err != nil {
		return nil, err
	}
	return s.do(ctx, http.MethodGet, p, nil, true)
}

// ── Namespaces ────────────────────────────────────────────────────────

// CreateNamespaceOptions configures CreateNamespace. DeclaredCapacity, when
// set, is the most records the namespace may hold.
type CreateNamespaceOptions struct {
	DeclaredCapacity *int64
}

// CreateNamespace creates a namespace. An upsert also creates its namespace;
// create one explicitly to set a declared capacity.
func (s *VectorIndexesService) CreateNamespace(ctx context.Context, index, name string, options CreateNamespaceOptions) (map[string]any, error) {
	p, err := indexPath(index, "/namespaces/create")
	if err != nil {
		return nil, err
	}
	body := jsonObject{}.set("name", name).
		setIf(options.DeclaredCapacity != nil, "declared_capacity", options.DeclaredCapacity)
	return s.do(ctx, http.MethodPost, p, body, false)
}

// DescribeNamespace returns one namespace, with its record_count.
func (s *VectorIndexesService) DescribeNamespace(ctx context.Context, index, name string) (map[string]any, error) {
	p, err := indexPath(index, "/namespaces/describe")
	if err != nil {
		return nil, err
	}
	return s.do(ctx, http.MethodPost, p, jsonObject{}.set("name", name), true)
}

// ListNamespacesOptions selects a page of namespaces. Cursor is a previous
// page's next_cursor.
type ListNamespacesOptions struct {
	Prefix string
	Limit  int
	Cursor string
}

// ListNamespaces returns one page of an index's namespaces:
// {"namespaces": [...], "next_cursor": ...}.
func (s *VectorIndexesService) ListNamespaces(ctx context.Context, index string, options ListNamespacesOptions) (map[string]any, error) {
	p, err := indexPath(index, "/namespaces/list")
	if err != nil {
		return nil, err
	}
	body := jsonObject{}.
		setIf(options.Prefix != "", "prefix", options.Prefix).
		setIf(options.Limit != 0, "limit", options.Limit).
		setIf(options.Cursor != "", "cursor", options.Cursor)
	return s.do(ctx, http.MethodPost, p, body, true)
}

// AllNamespaces yields every namespace of an index from ListNamespaces pages,
// starting at options.Cursor, one page in memory at a time.
func (s *VectorIndexesService) AllNamespaces(ctx context.Context, index string, options ListNamespacesOptions) iter.Seq2[map[string]any, error] {
	return pages(ctx, options.Cursor, func(cursor string) (map[string]any, []byte, error) {
		page := options
		page.Cursor = cursor
		answer, err := s.ListNamespaces(ctx, index, page)
		return answer, nil, err
	}, objectItems("namespaces"))
}

// ConfigureNamespace sets a namespace's declared capacity, or removes it when
// declaredCapacity is nil (sent as null). A value below the records already
// held is refused (409).
func (s *VectorIndexesService) ConfigureNamespace(ctx context.Context, index, name string, declaredCapacity *int64) (map[string]any, error) {
	p, err := indexPath(index, "/namespaces/configure")
	if err != nil {
		return nil, err
	}
	body := jsonObject{}.set("name", name).set("declared_capacity", declaredCapacity)
	return s.do(ctx, http.MethodPost, p, body, false)
}

// DeleteNamespace deletes a namespace and every record in it. The engine
// requires the name to be confirmed; this method sends the confirmation, so
// calling it is the confirmation. The default namespace cannot be deleted
// (400, Reason "namespace_reserved"): clear it with DeleteAll.
func (s *VectorIndexesService) DeleteNamespace(ctx context.Context, index, name string) (map[string]any, error) {
	p, err := indexPath(index, "/namespaces/delete")
	if err != nil {
		return nil, err
	}
	return s.do(ctx, http.MethodPost, p, jsonObject{}.set("name", name).set("confirm", name), false)
}

// BuildIndexOptions configures BuildIndex. Seed, when set, fixes the build's
// random seed.
type BuildIndexOptions struct {
	Seed *int64
}

// BuildIndex builds a namespace's disk IVF-PQ index as a background job and
// returns {"job_id", "receipt"}. Poll GetJob.
func (s *VectorIndexesService) BuildIndex(ctx context.Context, index, namespace string, options BuildIndexOptions) (map[string]any, error) {
	p, err := indexPath(index, "/namespaces/build-index")
	if err != nil {
		return nil, err
	}
	body := jsonObject{}.set("namespace", namespace).setIf(options.Seed != nil, "seed", options.Seed)
	return s.do(ctx, http.MethodPost, p, body, false)
}

// ── Records ───────────────────────────────────────────────────────────

// UpsertOptions bounds each upsert request. Zero fields select the engine's
// limits: VectorUpsertMaxRecords records and VectorUpsertMaxBodyBytes bytes.
type UpsertOptions struct {
	BatchSize int
	MaxBytes  int
}

// UpsertBatchResult is one acknowledged upsert request. Offset and Count name
// its range in the caller's input; Receipt is the engine's write receipt.
type UpsertBatchResult struct {
	Offset           int64
	Count            int
	UpsertedCount    int
	NamespaceCreated bool
	Receipt          any
}

// UpsertResult totals the acknowledged requests of an Upsert. Receipts holds
// one write receipt per request, in order.
type UpsertResult struct {
	UpsertedCount    int
	NamespaceCreated bool
	Batches          int
	Receipts         []any
}

// Upsert writes records, replacing any record with the same id, and totals
// the acknowledged requests.
//
// Each record is a map such as {"id": "p1", "values": [...], "sparse_values":
// {"indices": [...], "values": [...]}, "metadata": {...}, "text": {...}}; only
// "id" is always required. An upsert replaces the whole record. The namespace
// is created on its first write. FromSlice turns a slice into records.
//
// The input is read lazily and sent in requests of at most options.BatchSize
// records and options.MaxBytes bytes, in order, each record exactly once. Each
// request commits on its own. A failure returns a *BatchWriteError naming the
// input range that was not acknowledged (Offset, Count, and Outcome "not_sent"
// or "unknown"); every record before Offset is written, and the result counts
// them. Unwrap gives the cause. Nothing is sent again automatically.
func (s *VectorIndexesService) Upsert(ctx context.Context, index, namespace string, records iter.Seq2[map[string]any, error], options UpsertOptions) (*UpsertResult, error) {
	total := &UpsertResult{Receipts: []any{}}
	for batch, err := range s.UpsertBatches(ctx, index, namespace, records, options) {
		if err != nil {
			return total, err
		}
		total.UpsertedCount += batch.UpsertedCount
		total.NamespaceCreated = total.NamespaceCreated || batch.NamespaceCreated
		total.Batches++
		total.Receipts = append(total.Receipts, batch.Receipt)
	}
	return total, nil
}

// UpsertBatches is Upsert, lazily: it yields one UpsertBatchResult per
// acknowledged request and reads the input as it goes, so one batch is held in
// memory. Breaking the loop stops reading the input and sending requests.
func (s *VectorIndexesService) UpsertBatches(ctx context.Context, index, namespace string, records iter.Seq2[map[string]any, error], options UpsertOptions) iter.Seq2[UpsertBatchResult, error] {
	return func(yield func(UpsertBatchResult, error) bool) {
		p, err := indexPath(index, "/records/upsert")
		if err != nil {
			yield(UpsertBatchResult{}, err)
			return
		}
		batcher, err := newUpsertBatcher(namespace, options)
		if err != nil {
			yield(UpsertBatchResult{}, err)
			return
		}
		if records == nil {
			yield(UpsertBatchResult{}, errors.New("records must not be nil"))
			return
		}
		send := func(batch upsertBatch) bool {
			if err := ctx.Err(); err != nil {
				yield(UpsertBatchResult{}, &BatchWriteError{batch.offset, batch.count, "not_sent", err})
				return false
			}
			value, _, err := s.client.send(ctx, exchange{method: http.MethodPost, path: p, body: jsonBody(batch.body)})
			result, err := upsertAck(value, err, batch)
			if err != nil {
				yield(UpsertBatchResult{}, &BatchWriteError{batch.offset, batch.count, "unknown", err})
				return false
			}
			return yield(result, nil)
		}
		for record, inputErr := range records {
			if inputErr != nil {
				yield(UpsertBatchResult{}, &BatchWriteError{batcher.offset, len(batcher.items), "not_sent", inputErr})
				return
			}
			ready, err := batcher.add(record)
			if err != nil {
				yield(UpsertBatchResult{}, &BatchWriteError{batcher.offset, len(batcher.items) + 1, "not_sent", err})
				return
			}
			for _, batch := range ready {
				if !send(batch) {
					return
				}
			}
		}
		if len(batcher.items) > 0 {
			send(batcher.take())
		}
	}
}

// FetchOptions selects the fields Fetch returns. Metadata is returned unless
// IncludeMetadata is false; values and sparse values only when asked for.
type FetchOptions struct {
	IncludeValues   *bool
	IncludeSparse   *bool
	IncludeMetadata *bool
	MetadataFields  []string
	TextFields      []string
}

// Fetch reads records by id (1 to 1,000 unique ids) and returns {"records":
// {id: record}, "missing": [ids], "namespace", "receipt_position"}.
func (s *VectorIndexesService) Fetch(ctx context.Context, index, namespace string, ids []string, options FetchOptions) (map[string]any, error) {
	p, err := indexPath(index, "/records/fetch")
	if err != nil {
		return nil, err
	}
	if ids == nil {
		ids = []string{}
	}
	body := projection(jsonObject{}.set("namespace", namespace).set("ids", ids),
		options.IncludeValues, options.IncludeSparse, options.IncludeMetadata, options.MetadataFields, options.TextFields)
	return s.do(ctx, http.MethodPost, p, body, true)
}

// FetchByFilterOptions selects one page of the records matching a filter. Set
// exactly one of FilterExpr (operators such as $eq, $in, $gt and $and) and
// Filter (an equality map). Cursor is a previous page's next_cursor.
type FetchByFilterOptions struct {
	FilterExpr      any
	Filter          map[string]any
	Limit           int
	Cursor          string
	IncludeValues   *bool
	IncludeSparse   *bool
	IncludeMetadata *bool
	MetadataFields  []string
	TextFields      []string
}

// FetchByFilter returns one page of the records matching a filter, in
// ascending id order: {"records": {id: record}, "namespace", "next_cursor"}.
// A page can be short, or empty, and still carry next_cursor; only a page
// without one ends the listing.
func (s *VectorIndexesService) FetchByFilter(ctx context.Context, index, namespace string, options FetchByFilterOptions) (map[string]any, error) {
	p, body, err := fetchByFilterCall(index, namespace, options)
	if err != nil {
		return nil, err
	}
	return s.do(ctx, http.MethodPost, p, body, true)
}

// AllRecords yields every record matching a filter, in the order the server
// sends them, from FetchByFilter pages starting at options.Cursor. One page is
// held at a time. It is not a snapshot: each page reads the records as they
// are when it is fetched.
func (s *VectorIndexesService) AllRecords(ctx context.Context, index, namespace string, options FetchByFilterOptions) iter.Seq2[map[string]any, error] {
	return pages(ctx, options.Cursor, func(cursor string) (map[string]any, []byte, error) {
		page := options
		page.Cursor = cursor
		p, body, err := fetchByFilterCall(index, namespace, page)
		if err != nil {
			return nil, nil, err
		}
		return s.send(ctx, http.MethodPost, p, body, true)
	}, recordItems)
}

func fetchByFilterCall(index, namespace string, options FetchByFilterOptions) (string, jsonObject, error) {
	p, err := indexPath(index, "/records/fetch-by-filter")
	if err != nil {
		return "", nil, err
	}
	body, err := selector(jsonObject{}.set("namespace", namespace), options.FilterExpr, options.Filter, true)
	if err != nil {
		return "", nil, err
	}
	body = body.
		setIf(options.Limit != 0, "limit", options.Limit).
		setIf(options.Cursor != "", "cursor", options.Cursor)
	body = projection(body, options.IncludeValues, options.IncludeSparse, options.IncludeMetadata, options.MetadataFields, options.TextFields)
	return p, body, nil
}

// ListIDsOptions selects a page of record ids. Cursor is a previous page's
// next_cursor.
type ListIDsOptions struct {
	Prefix string
	Limit  int
	Cursor string
}

// ListIDs returns one page of record ids in ascending byte order:
// {"ids": [...], "next_cursor": ...}.
func (s *VectorIndexesService) ListIDs(ctx context.Context, index, namespace string, options ListIDsOptions) (map[string]any, error) {
	p, err := indexPath(index, "/records/list-ids")
	if err != nil {
		return nil, err
	}
	body := jsonObject{}.set("namespace", namespace).
		setIf(options.Prefix != "", "prefix", options.Prefix).
		setIf(options.Limit != 0, "limit", options.Limit).
		setIf(options.Cursor != "", "cursor", options.Cursor)
	return s.do(ctx, http.MethodPost, p, body, true)
}

// AllIDs yields every record id of a namespace from ListIDs pages, starting at
// options.Cursor, one page in memory at a time.
func (s *VectorIndexesService) AllIDs(ctx context.Context, index, namespace string, options ListIDsOptions) iter.Seq2[string, error] {
	return pages(ctx, options.Cursor, func(cursor string) (map[string]any, []byte, error) {
		page := options
		page.Cursor = cursor
		answer, err := s.ListIDs(ctx, index, namespace, page)
		return answer, nil, err
	}, idItems)
}

// UpdateOptions changes one record in place. Set at least one change. Fields
// that are not named are kept. MetadataMerge sets keys, MetadataReplace
// replaces the whole map and MetadataRemove drops keys. With IfRevisionToken
// (from a fetch or a receipt) the update applies only if the record has not
// changed since.
type UpdateOptions struct {
	Values          []float64
	SparseValues    map[string]any
	MetadataMerge   map[string]any
	MetadataReplace map[string]any
	MetadataRemove  []string
	TextSet         map[string]string
	TextRemove      []string
	IfRevisionToken string
}

// Update changes one record in place and returns {"receipt"}. Unlike Upsert,
// fields that are not named are kept. An absent record returns Code
// "record_not_found".
func (s *VectorIndexesService) Update(ctx context.Context, index, namespace, id string, options UpdateOptions) (map[string]any, error) {
	p, err := indexPath(index, "/records/update")
	if err != nil {
		return nil, err
	}
	changes := jsonObject{}.
		setIf(options.Values != nil, "values", options.Values).
		setIf(options.SparseValues != nil, "sparse_values", options.SparseValues).
		setIf(options.MetadataMerge != nil, "metadata_merge", options.MetadataMerge).
		setIf(options.MetadataReplace != nil, "metadata_replace", options.MetadataReplace).
		setIf(options.MetadataRemove != nil, "metadata_remove", options.MetadataRemove).
		setIf(options.TextSet != nil, "text_set", options.TextSet).
		setIf(options.TextRemove != nil, "text_remove", options.TextRemove)
	if len(changes) == 0 {
		return nil, errors.New("Update needs at least one change")
	}
	body := append(jsonObject{}.set("namespace", namespace).set("id", id), changes...).
		setIf(options.IfRevisionToken != "", "if_revision_token", options.IfRevisionToken)
	return s.do(ctx, http.MethodPost, p, body, false)
}

// UpdateByFilterOptions changes the metadata of every record matching a
// filter. Set exactly one of FilterExpr and Filter, and at least one of
// MetadataMerge and MetadataRemove. DryRun writes nothing.
type UpdateByFilterOptions struct {
	FilterExpr     any
	Filter         map[string]any
	MetadataMerge  map[string]any
	MetadataRemove []string
	DryRun         bool
}

// UpdateByFilter runs as a background job and returns {"job_id", "receipt"};
// poll GetJob. A job updates at most 100,000 records. With DryRun nothing is
// written: {"dry_run": true, "matched_records", "sample_ids"}.
func (s *VectorIndexesService) UpdateByFilter(ctx context.Context, index, namespace string, options UpdateByFilterOptions) (map[string]any, error) {
	p, err := indexPath(index, "/records/update")
	if err != nil {
		return nil, err
	}
	body, err := selector(jsonObject{}.set("namespace", namespace), options.FilterExpr, options.Filter, true)
	if err != nil {
		return nil, err
	}
	if options.MetadataMerge == nil && options.MetadataRemove == nil {
		return nil, errors.New("UpdateByFilter needs MetadataMerge or MetadataRemove")
	}
	body = body.
		setIf(options.MetadataMerge != nil, "metadata_merge", options.MetadataMerge).
		setIf(options.MetadataRemove != nil, "metadata_remove", options.MetadataRemove).
		setIf(options.DryRun, "dry_run", true)
	return s.do(ctx, http.MethodPost, p, body, false)
}

// Delete deletes records by id (1 to 1,000 unique ids) and returns
// {"deleted", "missing", "receipt"}. An id with no record counts as missing.
func (s *VectorIndexesService) Delete(ctx context.Context, index, namespace string, ids []string) (map[string]any, error) {
	p, err := indexPath(index, "/records/delete")
	if err != nil {
		return nil, err
	}
	if ids == nil {
		ids = []string{}
	}
	return s.do(ctx, http.MethodPost, p, jsonObject{}.set("namespace", namespace).set("ids", ids), false)
}

// DeleteByFilterOptions selects the records DeleteByFilter deletes. Set
// exactly one of FilterExpr and Filter. DryRun writes nothing.
type DeleteByFilterOptions struct {
	FilterExpr any
	Filter     map[string]any
	DryRun     bool
}

// DeleteByFilter deletes every record of a namespace that matches a filter, as
// a background job, and returns {"job_id", "receipt"}; poll GetJob. With
// DryRun nothing is written: {"dry_run": true, "matched_records", "sample_ids"}.
func (s *VectorIndexesService) DeleteByFilter(ctx context.Context, index, namespace string, options DeleteByFilterOptions) (map[string]any, error) {
	p, err := indexPath(index, "/records/delete")
	if err != nil {
		return nil, err
	}
	body, err := selector(jsonObject{}.set("namespace", namespace), options.FilterExpr, options.Filter, true)
	if err != nil {
		return nil, err
	}
	return s.do(ctx, http.MethodPost, p, body.setIf(options.DryRun, "dry_run", true), false)
}

// DeleteAllOptions configures DeleteAll. DryRun writes nothing.
type DeleteAllOptions struct {
	DryRun bool
}

// DeleteAll deletes every record of a namespace and keeps the namespace. The
// engine requires the namespace to be confirmed; this method sends the
// confirmation, so calling it is the confirmation. With DryRun nothing is
// written: {"dry_run": true, "record_count"}.
func (s *VectorIndexesService) DeleteAll(ctx context.Context, index, namespace string, options DeleteAllOptions) (map[string]any, error) {
	p, err := indexPath(index, "/records/delete")
	if err != nil {
		return nil, err
	}
	body := jsonObject{}.set("namespace", namespace).set("all", true).set("confirm", namespace).
		setIf(options.DryRun, "dry_run", true)
	return s.do(ctx, http.MethodPost, p, body, false)
}

// CountOptions narrows Count to the records matching a filter. Set at most one
// of FilterExpr and Filter; leave both unset to count every record.
type CountOptions struct {
	FilterExpr any
	Filter     map[string]any
}

// Count returns the exact number of records: {"count", "namespace"}.
func (s *VectorIndexesService) Count(ctx context.Context, index, namespace string, options CountOptions) (map[string]any, error) {
	p, err := indexPath(index, "/records/count")
	if err != nil {
		return nil, err
	}
	body, err := selector(jsonObject{}.set("namespace", namespace), options.FilterExpr, options.Filter, false)
	if err != nil {
		return nil, err
	}
	return s.do(ctx, http.MethodPost, p, body, true)
}

// ── Jobs ──────────────────────────────────────────────────────────────

// GetJob returns a background job: {"job_id", "kind", "state", "progress",
// ...}. It is finished once state is no longer "queued" or "running".
func (s *VectorIndexesService) GetJob(ctx context.Context, index, jobID string) (map[string]any, error) {
	p, err := jobPath(index, jobID, "")
	if err != nil {
		return nil, err
	}
	return s.do(ctx, http.MethodGet, p, nil, true)
}

// CancelJob stops a job's further rounds. It is not a rollback: records the
// job already changed stay changed.
func (s *VectorIndexesService) CancelJob(ctx context.Context, index, jobID string) (map[string]any, error) {
	p, err := jobPath(index, jobID, "/cancel")
	if err != nil {
		return nil, err
	}
	return s.do(ctx, http.MethodPost, p, nil, false)
}

// ── Search ────────────────────────────────────────────────────────────

// IndexSearchOptions is one search. K is required, and at least one query leg:
//
//   - dense: Vector, or RecordID (the stored vector of that record), with
//     Quality "fast", "high_recall" (the server default) or "exact"; or the
//     whole leg as Dense (which also takes "candidates");
//   - Sparse: {"indices": [...], "values": [...]}, with an optional "scorer"
//     ("cosine" or "dot") and "candidates";
//   - Lexical: {"field": ..., "query": ...}, BM25 over a declared text field,
//     with an optional "match" ("terms" or "phrase").
//
// One leg is a "semantic" (dense or sparse) or "lexical" search; two or more
// are "hybrid", fused by reciprocal rank (Fusion {"method": "rrf", "rrf_k": 60}
// is the server default). Mode overrides that choice.
//
// FilterExpr (operators) or Filter (an equality map) restricts the records
// ranked; nothing is filtered after the top K is cut. Search returns metadata
// only when IncludeMetadata is true. Unset fields are not sent.
type IndexSearchOptions struct {
	K                 int
	Vector            []float64
	RecordID          string
	Quality           string
	Dense             map[string]any
	Sparse            map[string]any
	Lexical           map[string]any
	Fusion            map[string]any
	Mode              string
	FilterExpr        any
	Filter            map[string]any
	IncludeValues     *bool
	IncludeSparse     *bool
	IncludeMetadata   *bool
	MetadataFields    []string
	TextFields        []string
	IncludeComponents *bool
	RequireComplete   *bool
	TimeoutMS         int
}

// Search returns the K best records, best first: {"hits": [{"id", "score",
// "rank", ...}], "legs", "completeness", "returned", ...}. Scores are always
// higher-is-better; completeness says whether an approximate leg may have
// missed records.
func (s *VectorIndexesService) Search(ctx context.Context, index, namespace string, options IndexSearchOptions) (map[string]any, error) {
	p, err := indexPath(index, "/search")
	if err != nil {
		return nil, err
	}
	if options.K < 1 {
		return nil, errors.New("K must be a positive integer")
	}
	if options.Vector != nil && options.RecordID != "" {
		return nil, errors.New("set at most one of Vector and RecordID")
	}
	shorthand := options.Vector != nil || options.RecordID != "" || options.Quality != ""
	if options.Dense != nil && shorthand {
		return nil, errors.New("set Dense, or Vector / RecordID / Quality, not both")
	}
	var dense any
	if options.Dense != nil {
		dense = options.Dense
	} else if shorthand {
		if options.Vector == nil && options.RecordID == "" {
			return nil, errors.New("Quality needs Vector or RecordID")
		}
		dense = jsonObject{}.
			setIf(options.Vector != nil, "vector", options.Vector).
			setIf(options.RecordID != "", "record_id", options.RecordID).
			setIf(options.Quality != "", "quality", options.Quality)
	}
	legs := jsonObject{}.
		setIf(dense != nil, "dense", dense).
		setIf(options.Sparse != nil, "sparse", options.Sparse).
		setIf(options.Lexical != nil, "lexical", options.Lexical)
	if len(legs) == 0 {
		return nil, errors.New("search needs a query: Vector, RecordID, Dense, Sparse or Lexical")
	}
	mode := options.Mode
	if mode == "" {
		switch {
		case len(legs) > 1:
			mode = "hybrid"
		case options.Lexical != nil:
			mode = "lexical"
		default:
			mode = "semantic"
		}
	}
	body := append(jsonObject{}.set("namespace", namespace).set("mode", mode).set("k", options.K), legs...).
		setIf(options.Fusion != nil, "fusion", options.Fusion)
	if body, err = selector(body, options.FilterExpr, options.Filter, false); err != nil {
		return nil, err
	}
	body = projection(body, options.IncludeValues, options.IncludeSparse, options.IncludeMetadata, options.MetadataFields, options.TextFields).
		setIf(options.IncludeComponents != nil, "include_components", options.IncludeComponents).
		setIf(options.RequireComplete != nil, "require_complete", options.RequireComplete).
		setIf(options.TimeoutMS != 0, "timeout_ms", options.TimeoutMS)
	return s.do(ctx, http.MethodPost, p, body, true)
}

// ── Requests ──────────────────────────────────────────────────────────

// do sends one call and returns its answer object. A nil body sends no body;
// jsonObject{} sends {}. read marks a call that is safe to send again.
func (s *VectorIndexesService) do(ctx context.Context, method, p string, body jsonObject, read bool) (map[string]any, error) {
	value, _, err := s.send(ctx, method, p, body, read)
	return value, err
}

func (s *VectorIndexesService) send(ctx context.Context, method, p string, body jsonObject, read bool) (map[string]any, []byte, error) {
	var data []byte
	if body != nil {
		var err error
		if data, err = body.MarshalJSON(); err != nil {
			return nil, nil, fmt.Errorf("encode request: %w", err)
		}
	}
	retries := 0
	if read {
		retries = s.client.retries
	}
	value, raw, err := s.client.send(ctx, exchange{method: method, path: p, body: jsonBody(data), retries: retries})
	object, err := objectResult(value, err)
	return object, raw, err
}

func jsonBody(data []byte) func() ([]byte, string, error) {
	if data == nil {
		return nil
	}
	return func() ([]byte, string, error) { return data, "application/json", nil }
}

func indexPath(index, suffix string) (string, error) {
	p, err := path("vector-indexes", index)
	if err != nil {
		return "", err
	}
	return p + suffix, nil
}

func jobPath(index, jobID, suffix string) (string, error) {
	p, err := path("vector-indexes", index, "jobs", jobID)
	if err != nil {
		return "", err
	}
	return p + suffix, nil
}

// selector adds filter_expr or filter. required refuses a call with neither.
func selector(body jsonObject, filterExpr any, filter map[string]any, required bool) (jsonObject, error) {
	if filterExpr != nil && filter != nil {
		return nil, errors.New("set at most one of FilterExpr and Filter")
	}
	if required && filterExpr == nil && filter == nil {
		return nil, errors.New("FilterExpr or Filter is required")
	}
	return body.setIf(filterExpr != nil, "filter_expr", filterExpr).setIf(filter != nil, "filter", filter), nil
}

func projection(body jsonObject, values, sparse, metadata *bool, metadataFields, textFields []string) jsonObject {
	return body.
		setIf(values != nil, "include_values", values).
		setIf(sparse != nil, "include_sparse", sparse).
		setIf(metadata != nil, "include_metadata", metadata).
		setIf(metadataFields != nil, "metadata_fields", metadataFields).
		setIf(textFields != nil, "text_fields", textFields)
}

// jsonObject is a request body whose keys keep the order they were added in.
type jsonObject []jsonField

type jsonField struct {
	key   string
	value any
}

func (o jsonObject) set(key string, value any) jsonObject {
	return append(o, jsonField{key, value})
}

func (o jsonObject) setIf(present bool, key string, value any) jsonObject {
	if !present {
		return o
	}
	return o.set(key, value)
}

// MarshalJSON writes compact JSON without HTML escaping. Like the server, it
// refuses NaN and infinities.
func (o jsonObject) MarshalJSON() ([]byte, error) {
	out := []byte{'{'}
	for i, field := range o {
		if i > 0 {
			out = append(out, ',')
		}
		key, err := encodeJSON(field.key)
		if err != nil {
			return nil, err
		}
		value, err := encodeJSON(field.value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field.key, err)
		}
		out = append(append(append(out, key...), ':'), value...)
	}
	return append(out, '}'), nil
}

func encodeJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// ── Upsert batching ───────────────────────────────────────────────────

type upsertBatch struct {
	offset int64
	count  int
	body   []byte
}

// upsertBatcher splits records into request bodies under both bounds. Every
// record lands in exactly one body, in input order, encoded once. Nothing is
// truncated: a record that cannot fit a request on its own is refused before
// its batch is sent.
type upsertBatcher struct {
	head     []byte
	overhead int
	limit    int
	maxBytes int
	items    [][]byte
	size     int
	offset   int64
}

func newUpsertBatcher(namespace string, options UpsertOptions) (*upsertBatcher, error) {
	limit, maxBytes := options.BatchSize, options.MaxBytes
	if limit == 0 {
		limit = VectorUpsertMaxRecords
	}
	if maxBytes == 0 {
		maxBytes = VectorUpsertMaxBodyBytes
	}
	if limit < 1 || limit > VectorUpsertMaxRecords {
		return nil, fmt.Errorf("BatchSize must be from 1 to %d", VectorUpsertMaxRecords)
	}
	name, err := encodeJSON(namespace)
	if err != nil {
		return nil, err
	}
	head := append(append([]byte(`{"namespace":`), name...), `,"records":[`...)
	overhead := len(head) + len("]}")
	if maxBytes <= overhead || maxBytes > VectorUpsertMaxBodyBytes {
		return nil, fmt.Errorf("MaxBytes must be from %d to %d", overhead+1, VectorUpsertMaxBodyBytes)
	}
	return &upsertBatcher{head: head, overhead: overhead, limit: limit, maxBytes: maxBytes, size: overhead}, nil
}

// add encodes record and returns the batches that are now ready to send.
func (b *upsertBatcher) add(record map[string]any) ([]upsertBatch, error) {
	if record == nil {
		return nil, errors.New("each record must be an object")
	}
	raw, err := encodeJSON(record)
	if err != nil {
		return nil, err
	}
	if b.overhead+len(raw) > b.maxBytes {
		return nil, fmt.Errorf("a record is %d bytes encoded, over the %d-byte request bound on its own", len(raw), b.maxBytes)
	}
	var ready []upsertBatch
	if len(b.items) > 0 && b.size+1+len(raw) > b.maxBytes {
		ready = append(ready, b.take())
	}
	if len(b.items) > 0 {
		b.size++
	}
	b.size += len(raw)
	b.items = append(b.items, raw)
	if len(b.items) == b.limit {
		ready = append(ready, b.take())
	}
	return ready, nil
}

func (b *upsertBatcher) take() upsertBatch {
	body := make([]byte, 0, b.size)
	body = append(body, b.head...)
	for i, item := range b.items {
		if i > 0 {
			body = append(body, ',')
		}
		body = append(body, item...)
	}
	body = append(body, ']', '}')
	batch := upsertBatch{b.offset, len(b.items), body}
	b.offset += int64(len(b.items))
	b.items, b.size = nil, b.overhead
	return batch
}

func upsertAck(value any, err error, batch upsertBatch) (UpsertBatchResult, error) {
	answer, err := objectResult(value, err)
	if err != nil {
		return UpsertBatchResult{}, err
	}
	number, ok := answer["upserted_count"].(json.Number)
	upserted, parseErr := number.Int64()
	if !ok || parseErr != nil || upserted < 0 || upserted > int64(batch.count) {
		return UpsertBatchResult{}, &Error{Kind: "protocol", cause: errors.New("upsert response must contain a valid upserted_count")}
	}
	created, _ := answer["namespace_created"].(bool)
	return UpsertBatchResult{batch.offset, batch.count, int(upserted), created, answer["receipt"]}, nil
}

// ── Paging ────────────────────────────────────────────────────────────

// pages yields the items of each page, following next_cursor from start until
// a page has none. One page is held at a time and nothing is prefetched.
func pages[T any](ctx context.Context, start string, fetch func(cursor string) (map[string]any, []byte, error), items func(page map[string]any, raw []byte) ([]T, error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		cursor := start
		for {
			page, raw, err := fetch(cursor)
			if err != nil {
				yield(zero, err)
				return
			}
			list, err := items(page, raw)
			if err != nil {
				yield(zero, err)
				return
			}
			for _, item := range list {
				if err := ctx.Err(); err != nil {
					yield(zero, err)
					return
				}
				if !yield(item, nil) {
					return
				}
			}
			next, err := nextCursor(cursor, page)
			if err != nil {
				yield(zero, err)
				return
			}
			if next == "" {
				return
			}
			cursor = next
		}
	}
}

// nextCursor returns the page's next_cursor, or "" on the last page. A cursor
// that is empty, not a string, or the one just sent is a protocol error.
func nextCursor(previous string, page map[string]any) (string, error) {
	value := page["next_cursor"]
	if value == nil {
		return "", nil
	}
	cursor, ok := value.(string)
	if !ok || cursor == "" || cursor == previous {
		return "", pageError("page response carried an invalid next_cursor")
	}
	return cursor, nil
}

func pageError(message string) error {
	return &Error{Kind: "protocol", Status: http.StatusOK, cause: errors.New(message)}
}

func objectItems(key string) func(map[string]any, []byte) ([]map[string]any, error) {
	return func(page map[string]any, _ []byte) ([]map[string]any, error) {
		list, ok := page[key].([]any)
		if !ok {
			return nil, pageError("page response must contain " + key)
		}
		items := make([]map[string]any, 0, len(list))
		for _, item := range list {
			object, ok := item.(map[string]any)
			if !ok {
				return nil, pageError(key + " must contain objects")
			}
			items = append(items, object)
		}
		return items, nil
	}
}

func idItems(page map[string]any, _ []byte) ([]string, error) {
	list, ok := page["ids"].([]any)
	if !ok {
		return nil, pageError("page response must contain ids")
	}
	ids := make([]string, 0, len(list))
	for _, item := range list {
		id, ok := item.(string)
		if !ok {
			return nil, pageError("ids must be strings")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// recordItems returns a page's records. The server keys them by id in an
// object; they are read from the raw answer so the server's order is kept.
func recordItems(page map[string]any, raw []byte) ([]map[string]any, error) {
	if _, ok := page["records"].(map[string]any); !ok {
		return objectItems("records")(page, raw)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, pageError("page response must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(top["records"]))
	decoder.UseNumber()
	if _, err := decoder.Token(); err != nil {
		return nil, pageError("records must be an object")
	}
	var records []map[string]any
	for decoder.More() {
		if _, err := decoder.Token(); err != nil {
			return nil, pageError("records must be an object")
		}
		var record map[string]any
		if err := decoder.Decode(&record); err != nil || record == nil {
			return nil, pageError("records must contain objects")
		}
		records = append(records, record)
	}
	return records, nil
}
