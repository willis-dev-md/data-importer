package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/willis-dev-md/data-importer/gen/dataimporter/v1"
	"github.com/willis-dev-md/data-importer/internal/mapping"
	"github.com/willis-dev-md/data-importer/internal/store"
)

const (
	// DefaultScanLimit is the per-call number of raw rows ListRecords maps (SCAN_LIMIT).
	DefaultScanLimit = 50000
	// DefaultPageSize is used when page_size is 0.
	DefaultPageSize = 50
	// MaxPageSize is the largest page; a bigger page_size is clamped to it.
	MaxPageSize = 1000

	maxKeyLen = 4096 // longest accepted page token payload / record key

	maxBatch = 500
	minBatch = 32
)

// ScanLimitFromEnv reads SCAN_LIMIT; unset or empty means DefaultScanLimit.
func ScanLimitFromEnv(getenv func(string) string) (int, error) {
	v := strings.TrimSpace(getenv("SCAN_LIMIT"))
	if v == "" {
		return DefaultScanLimit, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("SCAN_LIMIT must be a positive integer, got %q", v)
	}
	return n, nil
}

// DataOptions configure NewDataService.
type DataOptions struct {
	// ScanLimit bounds the raw rows mapped per ListRecords call; <= 0 means DefaultScanLimit.
	ScanLimit int
	Logger    *slog.Logger
}

// DataService implements dataimporter.v1.DataService: raw rows from the store,
// mapped with the source's active mapping on every read.
type DataService struct {
	pb.UnimplementedDataServiceServer
	st        *store.Store
	scanLimit int
	log       *slog.Logger
}

// NewDataService returns the DataService backed by st.
func NewDataService(st *store.Store, o DataOptions) *DataService {
	d := &DataService{st: st, scanLimit: o.ScanLimit, log: o.Logger}
	if d.scanLimit <= 0 {
		d.scanLimit = DefaultScanLimit
	}
	if d.log == nil {
		d.log = slog.Default()
	}
	return d
}

// RecordID is the id of a record: source_id, a colon, and the natural key (a
// JSON array of strings, see record.Key). GetRecord gets the source id as well,
// so the id is unambiguous even when a source id contains a colon.
func RecordID(sourceID, naturalKey string) string { return sourceID + ":" + naturalKey }

// internal maps a failed store call to a status. A Postgres data exception
// (SQLSTATE class 22, e.g. a NUL byte in a key) can only come from input and is
// INVALID_ARGUMENT; everything else is INTERNAL. In both cases the detail goes
// to the server log only, never to the client.
func (d *DataService) internal(op string, err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && strings.HasPrefix(pe.Code, "22") {
		d.log.Warn("request rejected by the database", "op", op, "sqlstate", pe.Code, "err", err)
		return invalid("request contains a value the database cannot store or compare")
	}
	d.log.Error("data service failure", "op", op, "err", err)
	return status.Errorf(codes.Internal, "%s failed", op)
}

// validKey reports whether s can be a natural key / page token payload: valid
// UTF-8, no NUL, and at most maxKeyLen bytes.
func validKey(s string) bool {
	return len(s) <= maxKeyLen && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// loadMapping returns the active mapping of the source: NOT_FOUND when the
// source does not exist, FAILED_PRECONDITION when it has no (valid) active mapping.
func (d *DataService) loadMapping(ctx context.Context, sourceID string) (*mapping.Mapping, error) {
	src, err := d.st.GetSource(ctx, sourceID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "source %q not found", sourceID)
	}
	if err != nil {
		return nil, d.internal("load source", err)
	}
	if src.ActiveMappingVersion == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "source %q has no active mapping", sourceID)
	}
	mp, err := d.st.ActiveMapping(ctx, sourceID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Errorf(codes.FailedPrecondition, "source %q has no active mapping", sourceID)
	}
	if err != nil {
		return nil, d.internal("load active mapping", err)
	}
	m, err := mapping.Parse(mp.Spec)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "active mapping v%d of source %q is not valid: %v", mp.Version, sourceID, err)
	}
	return m, nil
}

// GetRecord returns one mapped record.
func (d *DataService) GetRecord(ctx context.Context, req *pb.GetRecordRequest) (*pb.Record, error) {
	if req.GetSourceId() == "" || req.GetId() == "" {
		return nil, invalid("source_id and id are required")
	}
	m, err := d.loadMapping(ctx, req.SourceId)
	if err != nil {
		return nil, err
	}
	key, ok := strings.CutPrefix(req.Id, req.SourceId+":")
	if !ok || key == "" || !validKey(key) {
		return nil, status.Errorf(codes.NotFound, "record %q not found in source %q", req.Id, req.SourceId)
	}
	raw, err := d.st.GetRaw(ctx, req.SourceId, key)
	if errors.Is(err, store.ErrNotFound) || (err == nil && raw.DeletedAt != nil) {
		return nil, status.Errorf(codes.NotFound, "record %q not found in source %q", req.Id, req.SourceId)
	}
	if err != nil {
		return nil, d.internal("load record", err)
	}
	res := mapping.Eval(m, raw.Payload)
	rec, err := toRecord(m, raw, res)
	if err != nil {
		return nil, d.internal("convert record", err)
	}
	return rec, nil
}

// ListRecords walks the source's raw rows in natural-key order, maps each,
// filters on the mapped values and fills the page (see docs/api.md).
func (d *DataService) ListRecords(ctx context.Context, req *pb.ListRecordsRequest) (*pb.ListRecordsResponse, error) {
	switch {
	case req.GetSourceId() != "" && req.GetGroupId() != "":
		return nil, invalid("set either source_id or group_id, not both")
	case req.GetSourceId() == "" && req.GetGroupId() == "":
		return nil, invalid("source_id is required")
	case req.GetGroupId() != "":
		return nil, status.Error(codes.Unimplemented, "group_id alone is not implemented yet: groups arrive in a later milestone; pass source_id")
	}
	if len(req.Sort) > 0 {
		return nil, invalid("sort is not supported: records are returned in natural-key order")
	}
	if req.PageSize < 0 {
		return nil, invalid("page_size must not be negative, got %d", req.PageSize)
	}
	pageSize := int(req.PageSize)
	if pageSize == 0 {
		pageSize = DefaultPageSize
	}
	pageSize = min(pageSize, MaxPageSize) // larger values are clamped, not rejected
	filters, err := compileFilters(req.Filters)
	if err != nil {
		return nil, err
	}
	after := ""
	if req.PageToken != "" {
		b, err := base64.RawURLEncoding.DecodeString(req.PageToken)
		if err != nil || len(b) == 0 || !validKey(string(b)) {
			return nil, invalid("page_token is not valid")
		}
		after = string(b)
	}
	m, err := d.loadMapping(ctx, req.SourceId)
	if err != nil {
		return nil, err
	}

	resp := &pb.ListRecordsResponse{}
	scanned := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		need := pageSize - len(resp.Records)
		limit := min(d.scanLimit-scanned, max(minBatch, min(need, maxBatch)))
		batch, err := d.st.ListRawPage(ctx, req.SourceId, after, limit, req.IncludeDeleted)
		if err != nil {
			return nil, d.internal("read records", err)
		}
		exhausted := len(batch) < limit
		for i, raw := range batch {
			scanned++
			after = raw.Key
			res := mapping.Eval(m, raw.Payload)
			if matchAll(filters, res) {
				rec, err := toRecord(m, raw, res)
				if err != nil {
					return nil, d.internal("convert record", err)
				}
				resp.Records = append(resp.Records, rec)
			}
			if len(resp.Records) == pageSize {
				// Nothing follows when this was the last row the source has.
				if i == len(batch)-1 {
					more := !exhausted
					if more {
						next, err := d.st.ListRawPage(ctx, req.SourceId, after, 1, req.IncludeDeleted)
						if err != nil {
							return nil, d.internal("read records", err)
						}
						more = len(next) > 0
					}
					if !more {
						return resp, nil
					}
				}
				resp.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(after))
				return resp, nil
			}
		}
		if exhausted {
			return resp, nil
		}
		if scanned >= d.scanLimit {
			resp.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(after))
			return resp, nil
		}
	}
}

func matchAll(fs []*compiled, res mapping.Result) bool {
	if len(fs) == 0 {
		return true
	}
	errFields := make(map[string]bool, len(res.Errors))
	for _, e := range res.Errors {
		errFields[e.Field] = true
	}
	for _, f := range fs {
		if !f.matches(res, errFields) {
			return false
		}
	}
	return true
}

// toRecord builds the API record. Values in fields: string as string; int and
// decimal as number; bool as bool; date and period_start/period_end as ISO
// "YYYY-MM-DD" strings, timestamp as RFC 3339 UTC; null as null; dimensions as
// a nested struct of strings.
func toRecord(m *mapping.Mapping, raw store.RawRecord, res mapping.Result) (*pb.Record, error) {
	fields := make(map[string]*structpb.Value, len(res.Fields))
	for name, v := range res.Fields {
		if t, ok := v.(time.Time); ok {
			if f := m.Fields[name]; f != nil && f.Type == "timestamp" {
				fields[name] = structpb.NewStringValue(t.UTC().Format(time.RFC3339))
			} else {
				fields[name] = structpb.NewStringValue(t.UTC().Format(dateLayout))
			}
			continue
		}
		sv, err := toValue(v)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		fields[name] = sv
	}
	extra := make(map[string]*structpb.Value, len(res.Extra))
	for k, v := range res.Extra {
		sv, err := toValue(v)
		if err != nil {
			return nil, fmt.Errorf("extra %q: %w", k, err)
		}
		extra[k] = sv
	}
	rec := &pb.Record{
		Id:         RecordID(raw.SourceID, raw.Key),
		SourceId:   raw.SourceID,
		Fields:     &structpb.Struct{Fields: fields},
		Extra:      &structpb.Struct{Fields: extra},
		ImportedAt: timestamppb.New(raw.ImportedAt),
	}
	for _, e := range res.Errors {
		rec.MappingErrors = append(rec.MappingErrors, &pb.FieldError{Field: e.Field, Message: e.Code + ": " + e.Message})
	}
	return rec, nil
}

// toValue converts a Go value from Eval or a raw payload to a Struct value.
// json.Number becomes a number (integers beyond 2^53 lose precision).
func toValue(v any) (*structpb.Value, error) {
	switch x := v.(type) {
	case nil:
		return structpb.NewNullValue(), nil
	case string:
		return structpb.NewStringValue(x), nil
	case bool:
		return structpb.NewBoolValue(x), nil
	case int64:
		return structpb.NewNumberValue(float64(x)), nil
	case int:
		return structpb.NewNumberValue(float64(x)), nil
	case float64:
		return structpb.NewNumberValue(x), nil
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return structpb.NewStringValue(x.String()), nil
		}
		return structpb.NewNumberValue(f), nil
	case time.Time:
		return structpb.NewStringValue(x.UTC().Format(time.RFC3339)), nil
	case map[string]string:
		m := make(map[string]*structpb.Value, len(x))
		for k, s := range x {
			m[k] = structpb.NewStringValue(s)
		}
		return structpb.NewStructValue(&structpb.Struct{Fields: m}), nil
	case map[string]any:
		m := make(map[string]*structpb.Value, len(x))
		for k, e := range x {
			sv, err := toValue(e)
			if err != nil {
				return nil, err
			}
			m[k] = sv
		}
		return structpb.NewStructValue(&structpb.Struct{Fields: m}), nil
	case []any:
		l := make([]*structpb.Value, len(x))
		for i, e := range x {
			sv, err := toValue(e)
			if err != nil {
				return nil, err
			}
			l[i] = sv
		}
		return structpb.NewListValue(&structpb.ListValue{Values: l}), nil
	}
	return nil, fmt.Errorf("unsupported value type %T", v)
}
