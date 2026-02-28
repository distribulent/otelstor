package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/distribulent/otelstor/proto"
	"github.com/distribulent/otelstor/store"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"), 60)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func makeTestRS(service, name string, traceID, spanID []byte, start time.Time) *tracev1.ResourceSpans {
	return &tracev1.ResourceSpans{
		Resource: &resourcev1.Resource{
			Attributes: []*commonv1.KeyValue{{
				Key:   "service.name",
				Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: service}},
			}},
		},
		ScopeSpans: []*tracev1.ScopeSpans{{
			Spans: []*tracev1.Span{{
				TraceId:           traceID,
				SpanId:            spanID,
				Name:              name,
				StartTimeUnixNano: uint64(start.UnixNano()),
				EndTimeUnixNano:   uint64(start.Add(time.Second).UnixNano()),
				Status:            &tracev1.Status{Code: tracev1.Status_STATUS_CODE_OK},
			}},
		}},
	}
}

func newTraceServer(t *testing.T) *traceServer {
	t.Helper()
	return &traceServer{store: openTestStore(t)}
}

func newStatsServer(t *testing.T) (*statsServer, *store.Store) {
	t.Helper()
	s := openTestStore(t)
	ss := &statsServer{
		store:         s,
		port:          4317,
		httpPort:      4318,
		dataDir:       "/data",
		retentionDays: 60,
	}
	return ss, s
}

var testNow = time.Date(2026, 2, 19, 12, 0, 0, 0, time.UTC)

// ── handleHTTPExport ──────────────────────────────────────────────────────────

func TestHandleHTTPExport_ValidRequest(t *testing.T) {
	ts := newTraceServer(t)
	body, err := proto.Marshal(&collectortrace.ExportTraceServiceRequest{
		ResourceSpans: []*tracev1.ResourceSpans{
			makeTestRS("http-svc", "http-op", bytes.Repeat([]byte{0x01}, 16), bytes.Repeat([]byte{0x02}, 8), testNow),
		},
	})
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	w := httptest.NewRecorder()
	ts.handleHTTPExport(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status=%d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	// Response must parse as a valid ExportTraceServiceResponse.
	var resp collectortrace.ExportTraceServiceResponse
	if err := proto.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Errorf("response is not a valid ExportTraceServiceResponse: %v", err)
	}
	// Span must be persisted.
	spans, err := ts.store.GetSpans("http-svc", 10)
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if len(spans) != 1 {
		t.Errorf("got %d stored spans, want 1", len(spans))
	}
}

func TestHandleHTTPExport_WrongMethod(t *testing.T) {
	ts := newTraceServer(t)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/v1/traces", nil)
			w := httptest.NewRecorder()
			ts.handleHTTPExport(w, req)
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("method=%s: status=%d, want %d", method, w.Code, http.StatusMethodNotAllowed)
			}
		})
	}
}

func TestHandleHTTPExport_BadBody(t *testing.T) {
	ts := newTraceServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader([]byte("not valid protobuf")))
	w := httptest.NewRecorder()
	ts.handleHTTPExport(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleHTTPExport_EmptyValidBody(t *testing.T) {
	ts := newTraceServer(t)
	body, _ := proto.Marshal(&collectortrace.ExportTraceServiceRequest{})
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	w := httptest.NewRecorder()
	ts.handleHTTPExport(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status=%d, want %d for empty-but-valid request", w.Code, http.StatusOK)
	}
}

func TestHandleHTTPExport_MultipleResourceSpans(t *testing.T) {
	ts := newTraceServer(t)
	body, err := proto.Marshal(&collectortrace.ExportTraceServiceRequest{
		ResourceSpans: []*tracev1.ResourceSpans{
			makeTestRS("svc-x", "op-1", bytes.Repeat([]byte{0x10}, 16), bytes.Repeat([]byte{0x11}, 8), testNow),
			makeTestRS("svc-y", "op-2", bytes.Repeat([]byte{0x20}, 16), bytes.Repeat([]byte{0x21}, 8), testNow),
		},
	})
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	w := httptest.NewRecorder()
	ts.handleHTTPExport(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status=%d, want %d", w.Code, http.StatusOK)
	}
	for _, svc := range []string{"svc-x", "svc-y"} {
		spans, _ := ts.store.GetSpans(svc, 10)
		if len(spans) != 1 {
			t.Errorf("svc=%s: got %d stored spans, want 1", svc, len(spans))
		}
	}
}

// ── traceServer.Export (gRPC) ─────────────────────────────────────────────────

func TestExport_StoresSpan(t *testing.T) {
	ts := newTraceServer(t)
	_, err := ts.Export(context.Background(), &collectortrace.ExportTraceServiceRequest{
		ResourceSpans: []*tracev1.ResourceSpans{
			makeTestRS("grpc-svc", "grpc-op", bytes.Repeat([]byte{0x30}, 16), bytes.Repeat([]byte{0x31}, 8), testNow),
		},
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	spans, _ := ts.store.GetSpans("grpc-svc", 10)
	if len(spans) != 1 {
		t.Errorf("got %d stored spans after Export, want 1", len(spans))
	}
}

func TestExport_EmptyRequest(t *testing.T) {
	ts := newTraceServer(t)
	_, err := ts.Export(context.Background(), &collectortrace.ExportTraceServiceRequest{})
	if err != nil {
		t.Errorf("Export with empty request returned error: %v", err)
	}
}

func TestExport_MultipleSpans(t *testing.T) {
	ts := newTraceServer(t)
	var rss []*tracev1.ResourceSpans
	for i := range 5 {
		sp := make([]byte, 8)
		sp[7] = byte(i + 1)
		rss = append(rss, makeTestRS("multi-svc", fmt.Sprintf("op-%d", i), bytes.Repeat([]byte{byte(i + 1)}, 16), sp, testNow.Add(time.Duration(i)*time.Second)))
	}
	if _, err := ts.Export(context.Background(), &collectortrace.ExportTraceServiceRequest{ResourceSpans: rss}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	spans, _ := ts.store.GetSpans("multi-svc", 10)
	if len(spans) != 5 {
		t.Errorf("got %d spans after Export, want 5", len(spans))
	}
}

// ── spanSummaryToEntry ────────────────────────────────────────────────────────

func TestSpanSummaryToEntry(t *testing.T) {
	start := time.Date(2026, 2, 19, 10, 0, 0, 0, time.UTC)
	end := start.Add(250 * time.Millisecond)
	sum := store.SpanSummary{
		TraceID:      "aabbccdd",
		SpanID:       "11223344",
		ParentSpanID: "00112233",
		Name:         "test-op",
		Month:        "2026-02",
		StartTime:    start,
		EndTime:      end,
		Status:       int32(tracev1.Status_STATUS_CODE_ERROR),
	}
	entry := spanSummaryToEntry(sum)
	if entry.GetTraceId() != "aabbccdd" {
		t.Errorf("trace_id=%q, want aabbccdd", entry.GetTraceId())
	}
	if entry.GetSpanId() != "11223344" {
		t.Errorf("span_id=%q, want 11223344", entry.GetSpanId())
	}
	if entry.GetParentSpanId() != "00112233" {
		t.Errorf("parent_span_id=%q, want 00112233", entry.GetParentSpanId())
	}
	if entry.GetName() != "test-op" {
		t.Errorf("name=%q, want test-op", entry.GetName())
	}
	if entry.GetMonth() != "2026-02" {
		t.Errorf("month=%q, want 2026-02", entry.GetMonth())
	}
	if entry.GetStartTimeUnixNano() != start.UnixNano() {
		t.Errorf("start_time=%d, want %d", entry.GetStartTimeUnixNano(), start.UnixNano())
	}
	if entry.GetEndTimeUnixNano() != end.UnixNano() {
		t.Errorf("end_time=%d, want %d", entry.GetEndTimeUnixNano(), end.UnixNano())
	}
	if entry.GetStatusCode() != int32(tracev1.Status_STATUS_CODE_ERROR) {
		t.Errorf("status_code=%d, want ERROR", entry.GetStatusCode())
	}
}

// ── statsServer.GetStats ──────────────────────────────────────────────────────

func TestGetStats_Config(t *testing.T) {
	ss, _ := newStatsServer(t)
	resp, err := ss.GetStats(context.Background(), &pb.StatsRequest{})
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	cfg := resp.GetConfig()
	if cfg.GetPort() != 4317 {
		t.Errorf("port=%d, want 4317", cfg.GetPort())
	}
	if cfg.GetHttpPort() != 4318 {
		t.Errorf("http_port=%d, want 4318", cfg.GetHttpPort())
	}
	if cfg.GetDataDir() != "/data" {
		t.Errorf("data_dir=%q, want /data", cfg.GetDataDir())
	}
	if cfg.GetRetentionDays() != 60 {
		t.Errorf("retention_days=%d, want 60", cfg.GetRetentionDays())
	}
}

func TestGetStats_EmptyStore(t *testing.T) {
	ss, _ := newStatsServer(t)
	resp, err := ss.GetStats(context.Background(), &pb.StatsRequest{})
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if len(resp.GetServices()) != 0 {
		t.Errorf("got %d services on empty store, want 0", len(resp.GetServices()))
	}
}

func TestGetStats_WithData(t *testing.T) {
	ss, s := newStatsServer(t)
	if err := s.WriteResourceSpans(makeTestRS("stats-svc", "op", bytes.Repeat([]byte{0x40}, 16), bytes.Repeat([]byte{0x41}, 8), testNow)); err != nil {
		t.Fatalf("WriteResourceSpans: %v", err)
	}

	resp, err := ss.GetStats(context.Background(), &pb.StatsRequest{})
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if len(resp.GetServices()) != 1 {
		t.Fatalf("got %d services, want 1", len(resp.GetServices()))
	}
	svc := resp.GetServices()[0]
	if svc.GetName() != "stats-svc" {
		t.Errorf("service name=%q, want stats-svc", svc.GetName())
	}
	if len(svc.GetMonths()) == 0 {
		t.Error("expected at least one month bucket")
	}
	var total int64
	for _, m := range svc.GetMonths() {
		total += m.GetSpanCount()
	}
	if total != 1 {
		t.Errorf("total span count=%d, want 1", total)
	}
}

// ── statsServer.GetSpans ──────────────────────────────────────────────────────

func TestGetSpans_Server_Basic(t *testing.T) {
	ss, s := newStatsServer(t)
	for i := range 5 {
		sp := make([]byte, 8)
		sp[7] = byte(i + 1)
		rs := makeTestRS("span-svc", fmt.Sprintf("op-%d", i), bytes.Repeat([]byte{byte(i + 1)}, 16), sp, testNow.Add(time.Duration(i)*time.Second))
		if err := s.WriteResourceSpans(rs); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	resp, err := ss.GetSpans(context.Background(), &pb.GetSpansRequest{Service: "span-svc", Limit: 10})
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if resp.GetService() != "span-svc" {
		t.Errorf("service=%q, want span-svc", resp.GetService())
	}
	if len(resp.GetSpans()) != 5 {
		t.Errorf("got %d spans, want 5", len(resp.GetSpans()))
	}
}

func TestGetSpans_Server_Limit(t *testing.T) {
	ss, s := newStatsServer(t)
	for i := range 10 {
		sp := make([]byte, 8)
		sp[7] = byte(i + 1)
		rs := makeTestRS("lim-svc", "op", bytes.Repeat([]byte{byte(i + 1)}, 16), sp, testNow.Add(time.Duration(i)*time.Second))
		if err := s.WriteResourceSpans(rs); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	resp, err := ss.GetSpans(context.Background(), &pb.GetSpansRequest{Service: "lim-svc", Limit: 3})
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if len(resp.GetSpans()) != 3 {
		t.Errorf("got %d spans, want 3", len(resp.GetSpans()))
	}
}

func TestGetSpans_Server_UnknownService(t *testing.T) {
	ss, _ := newStatsServer(t)
	resp, err := ss.GetSpans(context.Background(), &pb.GetSpansRequest{Service: "no-such-svc", Limit: 10})
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if len(resp.GetSpans()) != 0 {
		t.Errorf("got %d spans for unknown service, want 0", len(resp.GetSpans()))
	}
}

func TestGetSpans_Server_SpanEntryFields(t *testing.T) {
	ss, s := newStatsServer(t)
	traceID := bytes.Repeat([]byte{0x55}, 16)
	spanID := bytes.Repeat([]byte{0x66}, 8)
	parentID := bytes.Repeat([]byte{0x77}, 8)
	rs := &tracev1.ResourceSpans{
		Resource: &resourcev1.Resource{
			Attributes: []*commonv1.KeyValue{{
				Key:   "service.name",
				Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "field-svc"}},
			}},
		},
		ScopeSpans: []*tracev1.ScopeSpans{{
			Spans: []*tracev1.Span{{
				TraceId:           traceID,
				SpanId:            spanID,
				ParentSpanId:      parentID,
				Name:              "field-op",
				StartTimeUnixNano: uint64(testNow.UnixNano()),
				EndTimeUnixNano:   uint64(testNow.Add(500 * time.Millisecond).UnixNano()),
				Status:            &tracev1.Status{Code: tracev1.Status_STATUS_CODE_ERROR},
			}},
		}},
	}
	if err := s.WriteResourceSpans(rs); err != nil {
		t.Fatalf("WriteResourceSpans: %v", err)
	}

	resp, err := ss.GetSpans(context.Background(), &pb.GetSpansRequest{Service: "field-svc", Limit: 10})
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if len(resp.GetSpans()) != 1 {
		t.Fatalf("got %d spans, want 1", len(resp.GetSpans()))
	}
	sp := resp.GetSpans()[0]
	if sp.GetTraceId() != hex.EncodeToString(traceID) {
		t.Errorf("trace_id=%q, want %q", sp.GetTraceId(), hex.EncodeToString(traceID))
	}
	if sp.GetSpanId() != hex.EncodeToString(spanID) {
		t.Errorf("span_id=%q, want %q", sp.GetSpanId(), hex.EncodeToString(spanID))
	}
	if sp.GetParentSpanId() != hex.EncodeToString(parentID) {
		t.Errorf("parent_span_id=%q, want %q", sp.GetParentSpanId(), hex.EncodeToString(parentID))
	}
	if sp.GetName() != "field-op" {
		t.Errorf("name=%q, want field-op", sp.GetName())
	}
	if sp.GetStatusCode() != int32(tracev1.Status_STATUS_CODE_ERROR) {
		t.Errorf("status_code=%d, want ERROR(%d)", sp.GetStatusCode(), tracev1.Status_STATUS_CODE_ERROR)
	}
}

// ── statsServer.GetSpanTree ───────────────────────────────────────────────────

func TestGetSpanTree_InvalidHex(t *testing.T) {
	ss, _ := newStatsServer(t)
	_, err := ss.GetSpanTree(context.Background(), &pb.GetSpanTreeRequest{SpanId: "not-valid-hex!"})
	if err == nil {
		t.Fatal("expected error for invalid hex span_id")
	}
	if grpcstatus.Code(err) != codes.InvalidArgument {
		t.Errorf("code=%v, want InvalidArgument", grpcstatus.Code(err))
	}
}

func TestGetSpanTree_NotFound(t *testing.T) {
	ss, _ := newStatsServer(t)
	_, err := ss.GetSpanTree(context.Background(), &pb.GetSpanTreeRequest{SpanId: "0102030405060708"})
	if err == nil {
		t.Fatal("expected NotFound error for unknown span_id")
	}
	if grpcstatus.Code(err) != codes.NotFound {
		t.Errorf("code=%v, want NotFound", grpcstatus.Code(err))
	}
}

func TestGetSpanTree_Found(t *testing.T) {
	ss, s := newStatsServer(t)
	traceID := bytes.Repeat([]byte{0xAA}, 16)
	anchorID := bytes.Repeat([]byte{0x01}, 8)
	childID := bytes.Repeat([]byte{0x02}, 8)

	if err := s.WriteResourceSpans(makeTestRS("tree-svc", "root", traceID, anchorID, testNow)); err != nil {
		t.Fatalf("write anchor: %v", err)
	}
	if err := s.WriteResourceSpans(makeTestRS("tree-svc", "child", traceID, childID, testNow.Add(30*time.Second))); err != nil {
		t.Fatalf("write child: %v", err)
	}

	resp, err := ss.GetSpanTree(context.Background(), &pb.GetSpanTreeRequest{
		SpanId: hex.EncodeToString(anchorID),
	})
	if err != nil {
		t.Fatalf("GetSpanTree: %v", err)
	}
	if resp.GetTraceId() != hex.EncodeToString(traceID) {
		t.Errorf("trace_id=%q, want %q", resp.GetTraceId(), hex.EncodeToString(traceID))
	}
	if len(resp.GetSpans()) != 2 {
		t.Errorf("got %d spans, want 2", len(resp.GetSpans()))
	}
}

func TestGetSpanTree_SpanEntryFields(t *testing.T) {
	ss, s := newStatsServer(t)
	traceID := bytes.Repeat([]byte{0xBB}, 16)
	spanID := bytes.Repeat([]byte{0x09}, 8)

	if err := s.WriteResourceSpans(makeTestRS("tree-svc2", "root", traceID, spanID, testNow)); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp, err := ss.GetSpanTree(context.Background(), &pb.GetSpanTreeRequest{
		SpanId: hex.EncodeToString(spanID),
	})
	if err != nil {
		t.Fatalf("GetSpanTree: %v", err)
	}
	if len(resp.GetSpans()) != 1 {
		t.Fatalf("got %d spans, want 1", len(resp.GetSpans()))
	}
	sp := resp.GetSpans()[0]
	if sp.GetName() != "root" {
		t.Errorf("name=%q, want root", sp.GetName())
	}
	if sp.GetSpanId() != hex.EncodeToString(spanID) {
		t.Errorf("span_id=%q, want %q", sp.GetSpanId(), hex.EncodeToString(spanID))
	}
	if sp.GetTraceId() != hex.EncodeToString(traceID) {
		t.Errorf("trace_id=%q, want %q", sp.GetTraceId(), hex.EncodeToString(traceID))
	}
}

// ── handleHTTPExport additional paths ─────────────────────────────────────────

// errReadCloser is a body that always errors on Read, used to simulate read failures.
type errReadCloser struct{ err error }

func (r *errReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (r *errReadCloser) Close() error             { return nil }

func TestHandleHTTPExport_ResponseContentType(t *testing.T) {
	ts := newTraceServer(t)
	body, _ := proto.Marshal(&collectortrace.ExportTraceServiceRequest{})
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	w := httptest.NewRecorder()
	ts.handleHTTPExport(w, req)
	if ct := w.Header().Get("Content-Type"); ct != "application/x-protobuf" {
		t.Errorf("Content-Type=%q, want application/x-protobuf", ct)
	}
}

func TestHandleHTTPExport_BodyReadError(t *testing.T) {
	ts := newTraceServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", nil)
	req.Body = &errReadCloser{err: fmt.Errorf("simulated read error")}
	w := httptest.NewRecorder()
	ts.handleHTTPExport(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want %d for body read error", w.Code, http.StatusBadRequest)
	}
}

func TestHandleHTTPExport_LogsWriteError(t *testing.T) {
	ts := newTraceServer(t)
	ts.store.Close() // force store failure
	body, _ := proto.Marshal(&collectortrace.ExportTraceServiceRequest{
		ResourceSpans: []*tracev1.ResourceSpans{
			makeTestRS("err-svc", "op", bytes.Repeat([]byte{0x01}, 16), bytes.Repeat([]byte{0x02}, 8), testNow),
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	w := httptest.NewRecorder()
	ts.handleHTTPExport(w, req)
	// Store error is logged but the HTTP response is still 200 OK.
	if w.Code != http.StatusOK {
		t.Errorf("status=%d, want %d (store write error is logged, not returned to client)", w.Code, http.StatusOK)
	}
}

// ── traceServer.Export store-error path ───────────────────────────────────────

func TestExport_LogsWriteError(t *testing.T) {
	ts := newTraceServer(t)
	ts.store.Close() // force store failure
	// Export must never return an error to the caller — it only logs the failure.
	_, err := ts.Export(context.Background(), &collectortrace.ExportTraceServiceRequest{
		ResourceSpans: []*tracev1.ResourceSpans{
			makeTestRS("err-svc", "op", bytes.Repeat([]byte{0x03}, 16), bytes.Repeat([]byte{0x04}, 8), testNow),
		},
	})
	if err != nil {
		t.Errorf("Export should not return error on store failure, got: %v", err)
	}
}

// ── statsServer gRPC Internal error paths ─────────────────────────────────────

func TestGetStats_StoreError(t *testing.T) {
	ss, s := newStatsServer(t)
	s.Close()
	_, err := ss.GetStats(context.Background(), &pb.StatsRequest{})
	if err == nil {
		t.Fatal("expected Internal error after store closed, got nil")
	}
	if grpcstatus.Code(err) != codes.Internal {
		t.Errorf("code=%v, want Internal", grpcstatus.Code(err))
	}
}

func TestGetSpans_Server_StoreError(t *testing.T) {
	ss, s := newStatsServer(t)
	s.Close()
	_, err := ss.GetSpans(context.Background(), &pb.GetSpansRequest{Service: "any", Limit: 10})
	if err == nil {
		t.Fatal("expected Internal error after store closed, got nil")
	}
	if grpcstatus.Code(err) != codes.Internal {
		t.Errorf("code=%v, want Internal", grpcstatus.Code(err))
	}
}

func TestGetSpanTree_StoreError(t *testing.T) {
	ss, s := newStatsServer(t)
	s.Close()
	_, err := ss.GetSpanTree(context.Background(), &pb.GetSpanTreeRequest{SpanId: "0102030405060708"})
	if err == nil {
		t.Fatal("expected Internal error after store closed, got nil")
	}
	if grpcstatus.Code(err) != codes.Internal {
		t.Errorf("code=%v, want Internal", grpcstatus.Code(err))
	}
}

// ── statsServer.GetSpans default limit ────────────────────────────────────────

func TestGetSpans_Server_DefaultLimit(t *testing.T) {
	ss, s := newStatsServer(t)
	for i := range 60 {
		sp := make([]byte, 8)
		sp[7] = byte(i + 1)
		rs := makeTestRS("dlim-svc", "op", bytes.Repeat([]byte{byte(i + 1)}, 16), sp, testNow.Add(time.Duration(i)*time.Second))
		if err := s.WriteResourceSpans(rs); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	// Limit 0 should default to 50 inside the store layer.
	resp, err := ss.GetSpans(context.Background(), &pb.GetSpansRequest{Service: "dlim-svc", Limit: 0})
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if len(resp.GetSpans()) != 50 {
		t.Errorf("got %d spans, want 50 (default limit)", len(resp.GetSpans()))
	}
}
