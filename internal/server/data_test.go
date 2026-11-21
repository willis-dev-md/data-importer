package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"gopkg.in/yaml.v3"

	pb "github.com/willis-dev-md/data-importer/gen/dataimporter/v1"
	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/importer"
	"github.com/willis-dev-md/data-importer/internal/mapping"
	"github.com/willis-dev-md/data-importer/internal/record"
	"github.com/willis-dev-md/data-importer/internal/source"
	"github.com/willis-dev-md/data-importer/internal/store"
	"github.com/willis-dev-md/data-importer/internal/testutil"
)

const repoRoot = "../.."

type dataEnv struct {
	pool *pgxpool.Pool
	st   *store.Store
}

func newDataEnv(t *testing.T) dataEnv {
	t.Helper()
	url := testutil.NewDatabaseURL(t)
	if _, err := store.Migrate(context.Background(), url, store.Up); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return dataEnv{pool: pool, st: store.New(pool)}
}

// serve starts a server with the real DataService and returns a client.
func (e dataEnv) serve(t *testing.T, scanLimit int) pb.DataServiceClient {
	t.Helper()
	srv := New(Options{Ping: okPing, Logger: quiet(), Data: NewDataService(e.st, DataOptions{ScanLimit: scanLimit, Logger: quiet()})})
	conn, stop := startBuf(t, srv)
	t.Cleanup(stop)
	return pb.NewDataServiceClient(conn)
}

func yamlToJSON(t *testing.T, data []byte) json.RawMessage {
	t.Helper()
	var generic any
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&generic); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// importONS applies the ONS example source and mapping and imports the fixture
// through importer.Run.
func (e dataEnv) importONS(t *testing.T) {
	t.Helper()
	fixture, err := filepath.Abs(filepath.Join(repoRoot, "testdata", "ons", "l55o.csv"))
	if err != nil {
		t.Fatal(err)
	}
	srcYAML, err := os.ReadFile(filepath.Join(repoRoot, "docs", "examples", "ons-l55o.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	srcYAML = regexp.MustCompile(`(?m)^location:.*$`).ReplaceAll(srcYAML, []byte("location: file://"+fixture))
	cfg, err := config.ParseSource(srcYAML)
	if err != nil {
		t.Fatal(err)
	}
	cfgJSON, err := importer.ConfigJSON(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mapYAML, err := os.ReadFile(filepath.Join(repoRoot, "docs", "examples", "ons-l55o.mapping.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := e.st.Apply(ctx, []store.ApplyItem{{
		Source: store.Source{ID: cfg.ID, Kind: cfg.Kind, Config: cfgJSON}, Spec: yamlToJSON(t, mapYAML),
	}}); err != nil {
		t.Fatal(err)
	}
	res, err := importer.Run(ctx, importer.Deps{
		Store:    e.st,
		Adapters: map[string]source.Adapter{config.KindCSV: source.NewCSV(source.FetchOptions{})},
	}, cfg.ID, importer.Options{})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Inserted != 642 {
		t.Fatalf("inserted = %d, want 642", res.Inserted)
	}
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func strV(s string) *structpb.Value  { return structpb.NewStringValue(s) }
func numV(n float64) *structpb.Value { return structpb.NewNumberValue(n) }
func flt(field string, op pb.FilterOp, v *structpb.Value) *pb.Filter {
	return &pb.Filter{Field: field, Op: op, Value: v}
}
func listV(vs ...*structpb.Value) *structpb.Value {
	return structpb.NewListValue(&structpb.ListValue{Values: vs})
}

const (
	opEQ  = pb.FilterOp_FILTER_OP_EQ
	opNEQ = pb.FilterOp_FILTER_OP_NEQ
	opGT  = pb.FilterOp_FILTER_OP_GT
	opGTE = pb.FilterOp_FILTER_OP_GTE
	opLT  = pb.FilterOp_FILTER_OP_LT
	opLTE = pb.FilterOp_FILTER_OP_LTE
	opCON = pb.FilterOp_FILTER_OP_CONTAINS
	opIN  = pb.FilterOp_FILTER_OP_IN
)

// listAll follows next_page_token to the end. It fails on a repeated id, on a
// non-advancing token and after an absurd number of pages.
func listAll(t *testing.T, c pb.DataServiceClient, req *pb.ListRecordsRequest) (recs []*pb.Record, pages []*pb.ListRecordsResponse) {
	t.Helper()
	ctx := ctxT(t)
	seen := map[string]bool{}
	lastTok := ""
	for i := 0; i < 5000; i++ {
		r, err := c.ListRecords(ctx, req)
		if err != nil {
			t.Fatalf("ListRecords: %v", err)
		}
		pages = append(pages, r)
		for _, rec := range r.Records {
			if seen[rec.Id] {
				t.Fatalf("duplicate record %q", rec.Id)
			}
			seen[rec.Id] = true
			recs = append(recs, rec)
		}
		if r.NextPageToken == "" {
			return recs, pages
		}
		if r.NextPageToken == lastTok {
			t.Fatalf("token did not advance: %q", lastTok)
		}
		lastTok = r.NextPageToken
		req = &pb.ListRecordsRequest{
			SourceId: req.SourceId, Filters: req.Filters, PageSize: req.PageSize,
			PageToken: r.NextPageToken, IncludeDeleted: req.IncludeDeleted,
		}
	}
	t.Fatal("too many pages")
	return nil, nil
}

func wantCode(t *testing.T, err error, code codes.Code, contains string) {
	t.Helper()
	if status.Code(err) != code {
		t.Fatalf("code = %v (%v), want %v", status.Code(err), err, code)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("error %q does not contain %q", err, contains)
	}
}

func TestONSListRecords(t *testing.T) {
	e := newDataEnv(t)
	e.importONS(t)
	c := e.serve(t, 0)
	month := flt("period_type", opEQ, strV("month"))

	tests := []struct {
		name    string
		filters []*pb.Filter
		want    int
	}{
		{"all", nil, 642},
		{"month", []*pb.Filter{month}, 454},
		{"month and value >= 3.5", []*pb.Filter{month, flt("value", opGTE, numV(3.5))}, 108},
		{"year", []*pb.Filter{flt("period_type", opEQ, strV("year"))}, 37},
		{"not month", []*pb.Filter{flt("period_type", opNEQ, strV("month"))}, 188},
		{"in year, quarter", []*pb.Filter{flt("period_type", opIN, listV(strV("year"), strV("quarter")))}, 188},
		{"series_id", []*pb.Filter{flt("series_id", opEQ, strV("L55O"))}, 642},
		{"series_id other", []*pb.Filter{flt("series_id", opEQ, strV("NOPE"))}, 0},
		{"measure contains", []*pb.Filter{flt("measure", opCON, strV("cpih"))}, 642},
		{"dimension unknown key", []*pb.Filter{flt("dimensions.nope", opEQ, strV("x"))}, 0},
		{"dimension unknown key NEQ", []*pb.Filter{flt("dimensions.nope", opNEQ, strV("x"))}, 0},
		{"period_start date range", []*pb.Filter{
			flt("period_start", opGTE, strV("2020-01-01")), flt("period_start", opLT, strV("2021-01-01")),
			month}, 12},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs, _ := listAll(t, c, &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: tt.filters, PageSize: 100})
			if len(recs) != tt.want {
				t.Fatalf("got %d records, want %d", len(recs), tt.want)
			}
		})
	}

	t.Run("record shape", func(t *testing.T) {
		recs, _ := listAll(t, c, &pb.ListRecordsRequest{SourceId: "ons-l55o", PageSize: 1000, Filters: []*pb.Filter{
			flt("period_type", opEQ, strV("year"))}})
		r := recs[0]
		f := r.Fields.Fields
		if f["series_id"].GetStringValue() != "L55O" || f["measure"].GetStringValue() != "cpih_annual_rate" ||
			f["period_type"].GetStringValue() != "year" {
			t.Errorf("fields = %v", r.Fields)
		}
		if _, ok := f["value"].GetKind().(*structpb.Value_NumberValue); !ok {
			t.Errorf("value is %T, want a number", f["value"].GetKind())
		}
		if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(f["period_start"].GetStringValue()) {
			t.Errorf("period_start = %v, want an ISO date", f["period_start"])
		}
		if d := f["dimensions"].GetStructValue(); d == nil || len(d.Fields) != 0 {
			t.Errorf("dimensions = %v, want an empty struct", f["dimensions"])
		}
		if r.ImportedAt == nil || r.ImportedAt.AsTime().IsZero() || r.SourceId != "ons-l55o" {
			t.Errorf("record = %v", r)
		}
		if len(r.MappingErrors) != 0 {
			t.Errorf("mapping errors = %v", r.MappingErrors)
		}
		// id round trip
		got, err := c.GetRecord(ctxT(t), &pb.GetRecordRequest{SourceId: "ons-l55o", Id: r.Id})
		if err != nil {
			t.Fatal(err)
		}
		if got.Id != r.Id || got.Fields.Fields["period_start"].GetStringValue() != f["period_start"].GetStringValue() {
			t.Errorf("GetRecord = %v, want %v", got, r)
		}
	})

	t.Run("every id round trips through GetRecord", func(t *testing.T) {
		recs, _ := listAll(t, c, &pb.ListRecordsRequest{SourceId: "ons-l55o", PageSize: 1000})
		for _, r := range recs {
			if _, err := c.GetRecord(ctxT(t), &pb.GetRecordRequest{SourceId: "ons-l55o", Id: r.Id}); err != nil {
				t.Fatalf("GetRecord(%q): %v", r.Id, err)
			}
		}
	})

	t.Run("page size", func(t *testing.T) {
		ctx := ctxT(t)
		for _, tt := range []struct {
			size int32
			want int
		}{{0, DefaultPageSize}, {7, 7}, {1000, 642}, {5000, 642}} {
			r, err := c.ListRecords(ctx, &pb.ListRecordsRequest{SourceId: "ons-l55o", PageSize: tt.size})
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Records) != tt.want {
				t.Errorf("page_size %d: %d records, want %d", tt.size, len(r.Records), tt.want)
			}
			if (tt.want < 642) != (r.NextPageToken != "") {
				t.Errorf("page_size %d: next_page_token = %q", tt.size, r.NextPageToken)
			}
		}
		_, err := c.ListRecords(ctx, &pb.ListRecordsRequest{SourceId: "ons-l55o", PageSize: -1})
		wantCode(t, err, codes.InvalidArgument, "page_size")
	})

	t.Run("last page exactly full has no token", func(t *testing.T) {
		recs, pages := listAll(t, c, &pb.ListRecordsRequest{SourceId: "ons-l55o", PageSize: 321})
		if len(recs) != 642 || len(pages) != 2 {
			t.Fatalf("%d records in %d pages", len(recs), len(pages))
		}
	})

	t.Run("token is base64 of the last natural key", func(t *testing.T) {
		r, err := c.ListRecords(ctxT(t), &pb.ListRecordsRequest{SourceId: "ons-l55o", PageSize: 3})
		if err != nil {
			t.Fatal(err)
		}
		b, err := base64.RawURLEncoding.DecodeString(r.NextPageToken)
		if err != nil {
			t.Fatal(err)
		}
		if want := RecordID("ons-l55o", string(b)); r.Records[2].Id != want {
			t.Errorf("last id = %q, token key gives %q", r.Records[2].Id, want)
		}
	})
}

func TestONSScanLimit(t *testing.T) {
	e := newDataEnv(t)
	e.importONS(t)
	c := e.serve(t, 7)
	filters := []*pb.Filter{flt("period_type", opEQ, strV("month")), flt("value", opGTE, numV(3.5))}
	recs, pages := listAll(t, c, &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: filters, PageSize: 100})
	if len(recs) != 108 {
		t.Fatalf("got %d records, want 108", len(recs))
	}
	short := 0
	for _, p := range pages[:len(pages)-1] {
		if len(p.Records) < 100 && p.NextPageToken != "" {
			short++
		}
	}
	if short == 0 || len(pages) < 642/7 {
		t.Errorf("expected many short pages with tokens, got %d short of %d pages", short, len(pages))
	}
	// no scan limit hit and nothing matches: empty and no token
	r, err := e.serve(t, 100000).ListRecords(ctxT(t), &pb.ListRecordsRequest{SourceId: "ons-l55o",
		Filters: []*pb.Filter{flt("series_id", opEQ, strV("NOPE"))}})
	if err != nil || len(r.Records) != 0 || r.NextPageToken != "" {
		t.Errorf("resp = %v, err = %v", r, err)
	}
}

func TestONSInvalidAndMissing(t *testing.T) {
	e := newDataEnv(t)
	e.importONS(t)
	c := e.serve(t, 0)
	if err := e.st.UpsertSource(context.Background(), store.Source{ID: "nomap", Kind: "csv"}); err != nil {
		t.Fatal(err)
	}
	b := func(v bool) *structpb.Value { return structpb.NewBoolValue(v) }

	list := []struct {
		name string
		req  *pb.ListRecordsRequest
		code codes.Code
		text string
	}{
		{"unknown field", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("nope", opEQ, strV("x"))}}, codes.InvalidArgument, `"nope"`},
		{"dimensions map itself", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("dimensions", opEQ, strV("x"))}}, codes.InvalidArgument, `"dimensions"`},
		{"dimensions. alone", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("dimensions.", opEQ, strV("x"))}}, codes.InvalidArgument, `"dimensions."`},
		{"dimension GT", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("dimensions.a", opGT, strV("x"))}}, codes.InvalidArgument, `"dimensions.a"`},
		{"dimension number", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("dimensions.a", opEQ, numV(1))}}, codes.InvalidArgument, `"dimensions.a"`},
		{"dimension null", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("dimensions.a", opEQ, structpb.NewNullValue())}}, codes.InvalidArgument, `"dimensions.a"`},
		{"empty field", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("", opEQ, strV("x"))}}, codes.InvalidArgument, "empty"},
		{"unspecified op", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("unit", pb.FilterOp_FILTER_OP_UNSPECIFIED, strV("x"))}}, codes.InvalidArgument, `"unit"`},
		{"unknown op", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("unit", 99, strV("x"))}}, codes.InvalidArgument, `"unit"`},
		{"no value", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{{Field: "unit", Op: opEQ}}}, codes.InvalidArgument, `"unit"`},
		{"number for string", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("period_type", opEQ, numV(1))}}, codes.InvalidArgument, `"period_type"`},
		{"string for decimal", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("value", opGT, strV("3"))}}, codes.InvalidArgument, `"value"`},
		{"bool for decimal", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("value", opEQ, b(true))}}, codes.InvalidArgument, `"value"`},
		{"number for date", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("period_start", opEQ, numV(2020))}}, codes.InvalidArgument, `"period_start"`},
		{"bad date", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("period_start", opEQ, strV("1 Jan 2020"))}}, codes.InvalidArgument, `"period_start"`},
		{"CONTAINS on number", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("value", opCON, numV(1))}}, codes.InvalidArgument, `"value"`},
		{"IN needs list", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("unit", opIN, strV("x"))}}, codes.InvalidArgument, `"unit"`},
		{"IN wrong element", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("unit", opIN, listV(strV("a"), numV(1)))}}, codes.InvalidArgument, `"unit"`},
		{"null with GT", &pb.ListRecordsRequest{SourceId: "ons-l55o", Filters: []*pb.Filter{flt("value", opGT, structpb.NewNullValue())}}, codes.InvalidArgument, `"value"`},
		{"sort", &pb.ListRecordsRequest{SourceId: "ons-l55o", Sort: []*pb.Sort{{Field: "value"}}}, codes.InvalidArgument, "sort"},
		{"bad token", &pb.ListRecordsRequest{SourceId: "ons-l55o", PageToken: "!!not base64!!"}, codes.InvalidArgument, "page_token"},
		{"token NUL AA", &pb.ListRecordsRequest{SourceId: "ons-l55o", PageToken: "AA"}, codes.InvalidArgument, "page_token"},
		{"token NUL AAAA", &pb.ListRecordsRequest{SourceId: "ons-l55o", PageToken: "AAAA"}, codes.InvalidArgument, "page_token"},
		{"token NUL AGE", &pb.ListRecordsRequest{SourceId: "ons-l55o", PageToken: "AGE"}, codes.InvalidArgument, "page_token"},
		{"token invalid UTF-8", &pb.ListRecordsRequest{SourceId: "ons-l55o", PageToken: base64.RawURLEncoding.EncodeToString([]byte{0xff, 0xfe})}, codes.InvalidArgument, "page_token"},
		{"token oversized", &pb.ListRecordsRequest{SourceId: "ons-l55o", PageToken: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte("a"), maxKeyLen+1))}, codes.InvalidArgument, "page_token"},
		{"token garbage", &pb.ListRecordsRequest{SourceId: "ons-l55o", PageToken: "%%%"}, codes.InvalidArgument, "page_token"},
		{"no source", &pb.ListRecordsRequest{}, codes.InvalidArgument, "source_id"},
		{"source and group", &pb.ListRecordsRequest{SourceId: "ons-l55o", GroupId: "g"}, codes.InvalidArgument, "group_id"},
		{"group only", &pb.ListRecordsRequest{GroupId: "g"}, codes.Unimplemented, "groups arrive in a later milestone"},
		{"unknown source", &pb.ListRecordsRequest{SourceId: "nope"}, codes.NotFound, `"nope"`},
		{"no active mapping", &pb.ListRecordsRequest{SourceId: "nomap"}, codes.FailedPrecondition, `"nomap"`},
	}
	for _, tt := range list {
		t.Run("list "+tt.name, func(t *testing.T) {
			_, err := c.ListRecords(ctxT(t), tt.req)
			wantCode(t, err, tt.code, tt.text)
		})
	}

	get := []struct {
		name string
		req  *pb.GetRecordRequest
		code codes.Code
	}{
		{"no ids", &pb.GetRecordRequest{}, codes.InvalidArgument},
		{"unknown source", &pb.GetRecordRequest{SourceId: "nope", Id: "nope:x"}, codes.NotFound},
		{"no active mapping", &pb.GetRecordRequest{SourceId: "nomap", Id: "nomap:x"}, codes.FailedPrecondition},
		{"unknown record", &pb.GetRecordRequest{SourceId: "ons-l55o", Id: `ons-l55o:["L55O","nope"]`}, codes.NotFound},
		{"id of another source", &pb.GetRecordRequest{SourceId: "ons-l55o", Id: `other:["L55O","1989"]`}, codes.NotFound},
		{"bare key", &pb.GetRecordRequest{SourceId: "ons-l55o", Id: `["L55O","1989"]`}, codes.NotFound},
		{"NUL in id", &pb.GetRecordRequest{SourceId: "ons-l55o", Id: "ons-l55o:a\x00b"}, codes.NotFound},
	}
	for _, tt := range get {
		t.Run("get "+tt.name, func(t *testing.T) {
			_, err := c.GetRecord(ctxT(t), tt.req)
			wantCode(t, err, tt.code, "")
		})
	}
}

func TestONSPaginationStableDuringImport(t *testing.T) {
	e := newDataEnv(t)
	e.importONS(t)
	c := e.serve(t, 0)
	ctx := ctxT(t)

	all, _ := listAll(t, c, &pb.ListRecordsRequest{SourceId: "ons-l55o", PageSize: 1000})
	want := map[string]bool{}
	for _, r := range all {
		want[r.Id] = true
	}

	first, err := c.ListRecords(ctx, &pb.ListRecordsRequest{SourceId: "ons-l55o", PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	// A later import adds one row that sorts before the cursor and one after it.
	runID, err := e.st.CreateRun(ctx, "ons-l55o", "manual")
	if err != nil {
		t.Fatal(err)
	}
	before, after := record.Key("A0"), record.Key("Z9")
	if _, err := e.st.UpsertRaw(ctx, runID, "ons-l55o", []record.Record{
		{Key: before, Payload: map[string]any{"period": "2020", "value": "1"}},
		{Key: after, Payload: map[string]any{"period": "2021", "value": "2"}},
	}); err != nil {
		t.Fatal(err)
	}

	seen := map[string]int{}
	for _, r := range first.Records {
		seen[r.Id]++
	}
	req := &pb.ListRecordsRequest{SourceId: "ons-l55o", PageSize: 100, PageToken: first.NextPageToken}
	rest, _ := listAll(t, c, req)
	for _, r := range rest {
		seen[r.Id]++
	}
	for id := range want {
		if seen[id] != 1 {
			t.Errorf("pre-existing record %q seen %d times", id, seen[id])
		}
	}
	if n := seen[RecordID("ons-l55o", after)]; n != 1 {
		t.Errorf("row added after the cursor seen %d times, want 1", n)
	}
	if n := seen[RecordID("ons-l55o", before)]; n != 0 {
		t.Errorf("row added before the cursor seen %d times, want 0", n)
	}
}

const handMapping = `
source: hand
version: 1
fields:
  series_id:    { from: sid, required: true }
  measure:      { const: m }
  statistic:    { from: stat }
  unit:         { from: unit }
  status:       { from: st }
  period_type:  { const: year }
  period_start: { from: start, type: date, format: "2006-01-02" }
  period_end:   { from: start, type: date, format: "2006-01-02" }
  value:        { from: v, type: decimal, null_markers: ["x"], flag: quality_flag }
dimensions:
  area: { from: area }
  sex:  { from: sex }
extra: keep
`

func (e dataEnv) handSource(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.st.Apply(ctx, []store.ApplyItem{{
		Source: store.Source{ID: "hand", Kind: "csv"}, Spec: yamlToJSON(t, []byte(handMapping)),
	}}); err != nil {
		t.Fatal(err)
	}
	runID, err := e.st.CreateRun(ctx, "hand", "manual")
	if err != nil {
		t.Fatal(err)
	}
	rows := []record.Record{
		{Key: record.Key("k1"), Payload: map[string]any{"sid": "A", "stat": "mean", "unit": "%", "st": "final", "v": "1.5", "area": "UK", "sex": "f", "start": "2020-01-01", "note": "n1"}},
		{Key: record.Key("k2"), Payload: map[string]any{"sid": "B", "v": "x", "area": "UK", "start": "2021-06-30"}},
		{Key: record.Key("k3"), Payload: map[string]any{"sid": "C", "v": "abc", "area": "GB", "start": "2022-01-01"}},
		{Key: record.Key("k4"), Payload: map[string]any{"v": "9", "area": "UK", "start": "2023-01-01"}},
		{Key: record.Key("k5"), Payload: map[string]any{"sid": "E", "v": "5", "area": "UK", "start": "2024-01-01"}},
	}
	if _, err := e.st.UpsertRaw(ctx, runID, "hand", rows); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE raw_records SET deleted_at = now() WHERE source_id = 'hand' AND natural_key = $1`, record.Key("k5")); err != nil {
		t.Fatal(err)
	}
}

func TestHandMadeFilters(t *testing.T) {
	e := newDataEnv(t)
	e.handSource(t)
	c := e.serve(t, 0)
	id := func(k string) string { return RecordID("hand", record.Key(k)) }

	tests := []struct {
		name    string
		filters []*pb.Filter
		deleted bool
		want    []string
	}{
		{"none, deleted hidden", nil, false, []string{"k1", "k2", "k3", "k4"}},
		{"none, deleted shown", nil, true, []string{"k1", "k2", "k3", "k4", "k5"}},
		{"dimension EQ", []*pb.Filter{flt("dimensions.area", opEQ, strV("UK"))}, false, []string{"k1", "k2", "k4"}},
		{"dimension NEQ", []*pb.Filter{flt("dimensions.area", opNEQ, strV("UK"))}, false, []string{"k3"}},
		{"dimension IN", []*pb.Filter{flt("dimensions.area", opIN, listV(strV("GB"), strV("FR")))}, false, []string{"k3"}},
		{"dimension absent on some rows", []*pb.Filter{flt("dimensions.sex", opNEQ, strV("m"))}, false, []string{"k1"}},
		{"dimension unknown key", []*pb.Filter{flt("dimensions.zzz", opEQ, strV("x"))}, false, nil},
		{"decimal GTE skips null and error", []*pb.Filter{flt("value", opGTE, numV(1))}, false, []string{"k1", "k4"}},
		{"decimal LT", []*pb.Filter{flt("value", opLT, numV(2))}, false, []string{"k1"}},
		{"decimal IN", []*pb.Filter{flt("value", opIN, listV(numV(9), numV(1.5)))}, false, []string{"k1", "k4"}},
		{"null EQ", []*pb.Filter{flt("value", opEQ, structpb.NewNullValue())}, false, []string{"k2"}},
		{"null NEQ", []*pb.Filter{flt("value", opNEQ, structpb.NewNullValue())}, false, []string{"k1", "k4"}},
		{"flag", []*pb.Filter{flt("quality_flag", opEQ, strV("x"))}, false, []string{"k2"}},
		{"measure", []*pb.Filter{flt("measure", opEQ, strV("m"))}, false, []string{"k1", "k2", "k3", "k4"}},
		{"statistic", []*pb.Filter{flt("statistic", opEQ, strV("mean"))}, false, []string{"k1"}},
		{"unit", []*pb.Filter{flt("unit", opEQ, strV("%"))}, false, []string{"k1"}},
		{"status", []*pb.Filter{flt("status", opCON, strV("ina"))}, false, []string{"k1"}},
		{"series_id missing on k4 (error) never matches NEQ", []*pb.Filter{flt("series_id", opNEQ, strV("A"))}, false, []string{"k2", "k3"}},
		{"date EQ", []*pb.Filter{flt("period_start", opEQ, strV("2021-06-30"))}, false, []string{"k2"}},
		{"date GT", []*pb.Filter{flt("period_start", opGT, strV("2021-06-30"))}, false, []string{"k3", "k4"}},
		{"string LTE", []*pb.Filter{flt("series_id", opLTE, strV("B"))}, false, []string{"k1", "k2"}},
		{"AND", []*pb.Filter{flt("dimensions.area", opEQ, strV("UK")), flt("value", opGT, numV(2))}, false, []string{"k4"}},
		{"failed field not in filter: record still returned", []*pb.Filter{flt("dimensions.area", opEQ, strV("GB"))}, false, []string{"k3"}},
		{"failed field in filter: no match", []*pb.Filter{flt("value", opEQ, numV(0))}, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs, _ := listAll(t, c, &pb.ListRecordsRequest{SourceId: "hand", Filters: tt.filters, IncludeDeleted: tt.deleted, PageSize: 2})
			var got []string
			for _, r := range recs {
				got = append(got, r.Id)
			}
			var want []string
			for _, k := range tt.want {
				want = append(want, id(k))
			}
			sort.Strings(got)
			sort.Strings(want)
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}

	t.Run("mapping errors are returned with the record", func(t *testing.T) {
		recs, _ := listAll(t, c, &pb.ListRecordsRequest{SourceId: "hand"})
		byID := map[string]*pb.Record{}
		for _, r := range recs {
			byID[r.Id] = r
		}
		errs := func(k string) []string {
			var out []string
			for _, e := range byID[id(k)].MappingErrors {
				out = append(out, e.Field+"|"+e.Message)
			}
			return out
		}
		if got := errs("k3"); len(got) != 1 || !strings.HasPrefix(got[0], "value|PARSE_FAILED: ") {
			t.Errorf("k3 errors = %v", got)
		}
		if got := errs("k4"); len(got) != 1 || !strings.HasPrefix(got[0], "series_id|MISSING_REQUIRED: ") {
			t.Errorf("k4 errors = %v", got)
		}
		if got := errs("k1"); len(got) != 0 {
			t.Errorf("k1 errors = %v", got)
		}
		k1 := byID[id("k1")]
		if k1.Extra.Fields["note"].GetStringValue() != "n1" {
			t.Errorf("extra = %v", k1.Extra)
		}
		if d := k1.Fields.Fields["dimensions"].GetStructValue().GetFields(); d["area"].GetStringValue() != "UK" || d["sex"].GetStringValue() != "f" || len(d) != 2 {
			t.Errorf("dimensions = %v", d)
		}
		k2 := byID[id("k2")]
		if v, ok := k2.Fields.Fields["value"]; !ok || v.GetKind() == nil {
			t.Errorf("k2 value = %v, want null", v)
		} else if _, isNull := v.GetKind().(*structpb.Value_NullValue); !isNull {
			t.Errorf("k2 value kind = %T, want null", v.GetKind())
		}
		if k2.Fields.Fields["quality_flag"].GetStringValue() != "x" {
			t.Errorf("k2 fields = %v", k2.Fields)
		}
	})

	t.Run("GetRecord with mapping errors and tombstones", func(t *testing.T) {
		r, err := c.GetRecord(ctxT(t), &pb.GetRecordRequest{SourceId: "hand", Id: id("k3")})
		if err != nil || len(r.MappingErrors) != 1 {
			t.Fatalf("GetRecord = %v, %v", r, err)
		}
		_, err = c.GetRecord(ctxT(t), &pb.GetRecordRequest{SourceId: "hand", Id: id("k5")})
		wantCode(t, err, codes.NotFound, "")
	})
}

func TestScanLimitFromEnv(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"", DefaultScanLimit, false}, {"  ", DefaultScanLimit, false}, {"10", 10, false},
		{"0", 0, true}, {"-3", 0, true}, {"abc", 0, true},
	}
	for _, tt := range tests {
		got, err := ScanLimitFromEnv(func(string) string { return tt.in })
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("SCAN_LIMIT=%q: got %d, %v", tt.in, got, err)
		}
	}
	if DefaultScanLimit != 50000 {
		t.Errorf("DefaultScanLimit = %d", DefaultScanLimit)
	}
}

func TestCanonicalKindsMatchSchema(t *testing.T) {
	c, err := mapping.LoadCanonical(filepath.Join(repoRoot, "canonical-schema.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, f := range c.Fields {
		if name == "dimensions" {
			if _, ok := canonicalKinds[name]; ok {
				t.Error("dimensions must not be in canonicalKinds")
			}
			continue
		}
		k, ok := canonicalKinds[name]
		if !ok {
			t.Errorf("canonical field %q is not filterable", name)
			continue
		}
		want := map[string]kind{"string": kString, "enum": kString, "decimal": kNumber, "int": kNumber, "bool": kBool, "date": kDate}[f.Type]
		if k != want {
			t.Errorf("field %q: kind %v, schema type %s", name, k, f.Type)
		}
	}
	for name := range canonicalKinds {
		if c.Fields[name] == nil {
			t.Errorf("%q is not a canonical field", name)
		}
	}
}

// TestMatchesTypes covers the int and bool paths, which no canonical field uses yet.
func TestMatchesTypes(t *testing.T) {
	res := mapping.Result{Fields: map[string]any{"n": int64(7), "b": true, "d": time.Date(2020, 5, 1, 0, 0, 0, 0, time.UTC)}}
	tests := []struct {
		name string
		c    *compiled
		want bool
	}{
		{"int EQ", &compiled{field: "n", op: opEQ, kind: kNumber, vals: []any{7.0}}, true},
		{"int GT", &compiled{field: "n", op: opGT, kind: kNumber, vals: []any{7.0}}, false},
		{"int GTE", &compiled{field: "n", op: opGTE, kind: kNumber, vals: []any{7.0}}, true},
		{"int IN", &compiled{field: "n", op: opIN, kind: kNumber, vals: []any{1.0, 7.0}}, true},
		{"int vs string value", &compiled{field: "n", op: opEQ, kind: kString, vals: []any{"7"}}, false},
		{"bool EQ", &compiled{field: "b", op: opEQ, kind: kBool, vals: []any{true}}, true},
		{"bool NEQ", &compiled{field: "b", op: opNEQ, kind: kBool, vals: []any{true}}, false},
		{"date LT", &compiled{field: "d", op: opLT, kind: kDate, vals: []any{"2020-05-02"}}, true},
		{"absent field", &compiled{field: "zz", op: opNEQ, kind: kString, vals: []any{"a"}}, false},
	}
	for _, tt := range tests {
		if got := tt.c.matches(res, nil); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestTokenOfAnotherSourceIsHarmless(t *testing.T) {
	e := newDataEnv(t)
	e.importONS(t)
	c := e.serve(t, 0)
	tok := base64.RawURLEncoding.EncodeToString([]byte(record.Key("zzz-not-a-key-of-ons")))
	resp, err := c.ListRecords(ctxT(t), &pb.ListRecordsRequest{SourceId: "ons-l55o", PageToken: tok})
	if err != nil {
		t.Fatalf("foreign token: %v", err)
	}
	if len(resp.Records) != 0 || resp.NextPageToken != "" {
		t.Errorf("records = %d, next = %q, want an empty page", len(resp.Records), resp.NextPageToken)
	}
}

func TestDimensionMappingErrorNeverMatches(t *testing.T) {
	e := newDataEnv(t)
	ctx := context.Background()
	spec := `
source: dimerr
version: 1
fields:
  series_id: { from: sid }
dimensions:
  area: { from: area, required: true }
extra: keep
`
	if _, err := e.st.Apply(ctx, []store.ApplyItem{{Source: store.Source{ID: "dimerr", Kind: "csv"}, Spec: yamlToJSON(t, []byte(spec))}}); err != nil {
		t.Fatal(err)
	}
	runID, err := e.st.CreateRun(ctx, "dimerr", "manual")
	if err != nil {
		t.Fatal(err)
	}
	rows := []record.Record{
		{Key: record.Key("ok"), Payload: map[string]any{"sid": "A", "area": "UK"}},
		{Key: record.Key("bad"), Payload: map[string]any{"sid": "B"}},
	}
	if _, err := e.st.UpsertRaw(ctx, runID, "dimerr", rows); err != nil {
		t.Fatal(err)
	}
	c := e.serve(t, 0)
	all, _ := listAll(t, c, &pb.ListRecordsRequest{SourceId: "dimerr"})
	var bad *pb.Record
	for _, r := range all {
		if r.Id == RecordID("dimerr", record.Key("bad")) {
			bad = r
		}
	}
	if bad == nil || len(bad.MappingErrors) == 0 {
		t.Fatalf("test setup: row bad must have a dimension mapping error, got %v", all)
	}
	for _, op := range []pb.FilterOp{opEQ, opNEQ, opIN} {
		val := strV("UK")
		if op == opIN {
			val = listV(strV("UK"))
		}
		recs, _ := listAll(t, c, &pb.ListRecordsRequest{SourceId: "dimerr", Filters: []*pb.Filter{flt("dimensions.area", op, val)}})
		for _, r := range recs {
			if r.Id == bad.Id {
				t.Errorf("op %v: record with a dimension mapping error matched", op)
			}
		}
	}
}
