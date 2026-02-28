package store

import (
	"bytes"
	"encoding/hex"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	bolt "go.etcd.io/bbolt"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"), 60)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// makeRS builds a ResourceSpans with a single span.
func makeRS(service, name string, traceID, spanID, parentID []byte, start, end time.Time, code tracev1.Status_StatusCode) *tracev1.ResourceSpans {
	var attrs []*commonv1.KeyValue
	if service != "" {
		attrs = []*commonv1.KeyValue{{
			Key:   "service.name",
			Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: service}},
		}}
	}
	return &tracev1.ResourceSpans{
		Resource: &resourcev1.Resource{Attributes: attrs},
		ScopeSpans: []*tracev1.ScopeSpans{{
			Spans: []*tracev1.Span{{
				TraceId:           traceID,
				SpanId:            spanID,
				ParentSpanId:      parentID,
				Name:              name,
				StartTimeUnixNano: uint64(start.UnixNano()),
				EndTimeUnixNano:   uint64(end.UnixNano()),
				Status:            &tracev1.Status{Code: code},
			}},
		}},
	}
}

// tid returns a 16-byte trace ID where every byte equals b.
func tid(b byte) []byte { return bytes.Repeat([]byte{b}, 16) }

// sid returns an 8-byte span ID where every byte equals b.
func sid(b byte) []byte { return bytes.Repeat([]byte{b}, 8) }

// now is a fixed point in time used across tests so tests remain deterministic.
var now = time.Date(2026, 2, 19, 12, 0, 0, 0, time.UTC)

// ── Open / Close ──────────────────────────────────────────────────────────────

func TestOpen_InvalidPath(t *testing.T) {
	_, err := Open("/nonexistent/path/does/not/exist/test.db", 60)
	if err == nil {
		t.Fatal("expected error for invalid path, got nil")
	}
}

func TestOpen_DefaultRetentionDays(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"), 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if s.retentionDays != DefaultRetentionDays {
		t.Errorf("retentionDays=%d, want %d", s.retentionDays, DefaultRetentionDays)
	}
}

// ── WriteResourceSpans ────────────────────────────────────────────────────────

func TestWrite_SingleSpan(t *testing.T) {
	s := newStore(t)
	rs := makeRS("svc", "op", tid(1), sid(1), nil, now, now.Add(time.Second), tracev1.Status_STATUS_CODE_OK)
	if err := s.WriteResourceSpans(rs); err != nil {
		t.Fatalf("WriteResourceSpans: %v", err)
	}
	spans, err := s.GetSpans("svc", 10)
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	sp := spans[0]
	if sp.Name != "op" {
		t.Errorf("name=%q, want %q", sp.Name, "op")
	}
	if sp.TraceID != hex.EncodeToString(tid(1)) {
		t.Errorf("trace_id=%q, want %q", sp.TraceID, hex.EncodeToString(tid(1)))
	}
	if sp.SpanID != hex.EncodeToString(sid(1)) {
		t.Errorf("span_id=%q, want %q", sp.SpanID, hex.EncodeToString(sid(1)))
	}
	if sp.Status != int32(tracev1.Status_STATUS_CODE_OK) {
		t.Errorf("status=%d, want %d", sp.Status, tracev1.Status_STATUS_CODE_OK)
	}
	if sp.Month != now.Format("2006-01") {
		t.Errorf("month=%q, want %q", sp.Month, now.Format("2006-01"))
	}
}

func TestWrite_ParentSpanID(t *testing.T) {
	s := newStore(t)
	parent := sid(0xAA)
	rs := makeRS("svc", "child", tid(1), sid(1), parent, now, now.Add(time.Second), tracev1.Status_STATUS_CODE_OK)
	if err := s.WriteResourceSpans(rs); err != nil {
		t.Fatalf("WriteResourceSpans: %v", err)
	}
	spans, _ := s.GetSpans("svc", 10)
	if len(spans) == 0 {
		t.Fatal("no spans returned")
	}
	if spans[0].ParentSpanID != hex.EncodeToString(parent) {
		t.Errorf("parent_span_id=%q, want %q", spans[0].ParentSpanID, hex.EncodeToString(parent))
	}
}

func TestWrite_MultipleSpansInBatch(t *testing.T) {
	s := newStore(t)
	rs := &tracev1.ResourceSpans{
		Resource: &resourcev1.Resource{
			Attributes: []*commonv1.KeyValue{{
				Key:   "service.name",
				Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "batch-svc"}},
			}},
		},
		ScopeSpans: []*tracev1.ScopeSpans{
			{Spans: []*tracev1.Span{
				{TraceId: tid(1), SpanId: sid(1), Name: "span-a", StartTimeUnixNano: uint64(now.UnixNano()), EndTimeUnixNano: uint64(now.Add(time.Second).UnixNano()), Status: &tracev1.Status{}},
				{TraceId: tid(1), SpanId: sid(2), Name: "span-b", StartTimeUnixNano: uint64(now.Add(time.Second).UnixNano()), EndTimeUnixNano: uint64(now.Add(2 * time.Second).UnixNano()), Status: &tracev1.Status{}},
			}},
			{Spans: []*tracev1.Span{
				{TraceId: tid(1), SpanId: sid(3), Name: "span-c", StartTimeUnixNano: uint64(now.Add(2 * time.Second).UnixNano()), EndTimeUnixNano: uint64(now.Add(3 * time.Second).UnixNano()), Status: &tracev1.Status{}},
			}},
		},
	}
	if err := s.WriteResourceSpans(rs); err != nil {
		t.Fatalf("WriteResourceSpans: %v", err)
	}
	spans, err := s.GetSpans("batch-svc", 10)
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if len(spans) != 3 {
		t.Errorf("got %d spans, want 3", len(spans))
	}
}

func TestWrite_NilResource_FallsBackToUnknown(t *testing.T) {
	s := newStore(t)
	rs := &tracev1.ResourceSpans{
		Resource: nil,
		ScopeSpans: []*tracev1.ScopeSpans{{
			Spans: []*tracev1.Span{{
				TraceId: tid(1), SpanId: sid(1),
				StartTimeUnixNano: uint64(now.UnixNano()),
				EndTimeUnixNano:   uint64(now.Add(time.Second).UnixNano()),
				Status:            &tracev1.Status{},
			}},
		}},
	}
	if err := s.WriteResourceSpans(rs); err != nil {
		t.Fatalf("WriteResourceSpans: %v", err)
	}
	spans, _ := s.GetSpans("unknown", 10)
	if len(spans) != 1 {
		t.Errorf("got %d spans for 'unknown', want 1", len(spans))
	}
}

// ── GetSpans ──────────────────────────────────────────────────────────────────

func TestGetSpans_Ordering(t *testing.T) {
	s := newStore(t)
	// Write spans at T, T+10s, T+20s, T+30s, T+40s.
	for i := range 5 {
		sp := sid(byte(i + 1))
		rs := makeRS("ord-svc", "op", tid(1), sp, nil,
			now.Add(time.Duration(i)*10*time.Second),
			now.Add(time.Duration(i)*10*time.Second+time.Second),
			tracev1.Status_STATUS_CODE_OK)
		if err := s.WriteResourceSpans(rs); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	spans, err := s.GetSpans("ord-svc", 10)
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if len(spans) != 5 {
		t.Fatalf("got %d spans, want 5", len(spans))
	}
	// Newest first: spans[0] should have the latest start time.
	for i := 1; i < len(spans); i++ {
		if spans[i-1].StartTime.Before(spans[i].StartTime) {
			t.Errorf("span[%d].StartTime=%v > span[%d].StartTime=%v — not newest-first",
				i-1, spans[i-1].StartTime, i, spans[i].StartTime)
		}
	}
}

func TestGetSpans_Limit(t *testing.T) {
	s := newStore(t)
	for i := range 8 {
		rs := makeRS("lim-svc", "op", tid(1), sid(byte(i+1)), nil,
			now.Add(time.Duration(i)*time.Second),
			now.Add(time.Duration(i)*time.Second+time.Second),
			tracev1.Status_STATUS_CODE_OK)
		if err := s.WriteResourceSpans(rs); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	spans, err := s.GetSpans("lim-svc", 3)
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if len(spans) != 3 {
		t.Errorf("got %d spans, want 3", len(spans))
	}
}

func TestGetSpans_DefaultLimit(t *testing.T) {
	s := newStore(t)
	// Write 55 spans; limit=0 should default to 50.
	for i := range 55 {
		rs := makeRS("dlim-svc", "op", tid(1), append(make([]byte, 7), byte(i+1)), nil,
			now.Add(time.Duration(i)*time.Second),
			now.Add(time.Duration(i)*time.Second+time.Second),
			tracev1.Status_STATUS_CODE_OK)
		if err := s.WriteResourceSpans(rs); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	spans, err := s.GetSpans("dlim-svc", 0)
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if len(spans) != 50 {
		t.Errorf("got %d spans, want 50 (default limit)", len(spans))
	}
}

func TestGetSpans_UnknownService(t *testing.T) {
	s := newStore(t)
	spans, err := s.GetSpans("no-such-service", 10)
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if len(spans) != 0 {
		t.Errorf("got %d spans for unknown service, want 0", len(spans))
	}
}

func TestGetSpans_MultipleMonths(t *testing.T) {
	s := newStore(t)
	jan := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	feb := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)

	rsJan := makeRS("mm-svc", "jan-op", tid(1), sid(1), nil, jan, jan.Add(time.Second), tracev1.Status_STATUS_CODE_OK)
	rsFeb := makeRS("mm-svc", "feb-op", tid(2), sid(2), nil, feb, feb.Add(time.Second), tracev1.Status_STATUS_CODE_OK)
	if err := s.WriteResourceSpans(rsJan); err != nil {
		t.Fatalf("write jan: %v", err)
	}
	if err := s.WriteResourceSpans(rsFeb); err != nil {
		t.Fatalf("write feb: %v", err)
	}

	spans, err := s.GetSpans("mm-svc", 10)
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(spans))
	}
	// Newest-first: February span should come before January.
	if spans[0].Name != "feb-op" {
		t.Errorf("first span name=%q, want feb-op (newest first)", spans[0].Name)
	}
	if spans[1].Name != "jan-op" {
		t.Errorf("second span name=%q, want jan-op", spans[1].Name)
	}
}

// ── BucketStats ───────────────────────────────────────────────────────────────

func TestBucketStats_Empty(t *testing.T) {
	s := newStore(t)
	infos, err := s.BucketStats()
	if err != nil {
		t.Fatalf("BucketStats: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("got %d services on empty store, want 0", len(infos))
	}
}

func TestBucketStats_Counts(t *testing.T) {
	s := newStore(t)
	// Write 3 spans for svc-a, 2 for svc-b (all same month).
	for i := range 3 {
		rs := makeRS("svc-a", "op", tid(byte(i)), sid(byte(i)), nil, now.Add(time.Duration(i)*time.Second), now.Add(time.Duration(i+1)*time.Second), tracev1.Status_STATUS_CODE_OK)
		if err := s.WriteResourceSpans(rs); err != nil {
			t.Fatalf("write svc-a %d: %v", i, err)
		}
	}
	for i := range 2 {
		rs := makeRS("svc-b", "op", tid(byte(i+10)), sid(byte(i+10)), nil, now.Add(time.Duration(i)*time.Second), now.Add(time.Duration(i+1)*time.Second), tracev1.Status_STATUS_CODE_OK)
		if err := s.WriteResourceSpans(rs); err != nil {
			t.Fatalf("write svc-b %d: %v", i, err)
		}
	}

	infos, err := s.BucketStats()
	if err != nil {
		t.Fatalf("BucketStats: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("got %d services, want 2", len(infos))
	}

	counts := map[string]int64{}
	for _, info := range infos {
		for _, m := range info.Months {
			counts[info.Name] += m.SpanCount
		}
	}
	if counts["svc-a"] != 3 {
		t.Errorf("svc-a span count=%d, want 3", counts["svc-a"])
	}
	if counts["svc-b"] != 2 {
		t.Errorf("svc-b span count=%d, want 2", counts["svc-b"])
	}
}

// ── GetSpanTree ───────────────────────────────────────────────────────────────

func TestGetSpanTree_NotFound(t *testing.T) {
	s := newStore(t)
	traceID, spans, err := s.GetSpanTree(sid(0xFF))
	if err != nil {
		t.Fatalf("GetSpanTree: %v", err)
	}
	if traceID != "" {
		t.Errorf("traceID=%q, want empty", traceID)
	}
	if len(spans) != 0 {
		t.Errorf("got %d spans, want 0", len(spans))
	}
}

func TestGetSpanTree_Basic(t *testing.T) {
	s := newStore(t)
	traceID := tid(0xBB)
	anchor := sid(0x01)
	child := sid(0x02)
	other := sid(0x03)

	// Anchor and child share the same trace ID.
	if err := s.WriteResourceSpans(makeRS("svc", "root", traceID, anchor, nil, now, now.Add(time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write anchor: %v", err)
	}
	if err := s.WriteResourceSpans(makeRS("svc", "child", traceID, child, anchor, now.Add(30*time.Second), now.Add(31*time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write child: %v", err)
	}
	// Different trace ID — must NOT appear in the result.
	if err := s.WriteResourceSpans(makeRS("svc", "unrelated", tid(0xCC), other, nil, now.Add(15*time.Second), now.Add(16*time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write unrelated: %v", err)
	}

	gotTraceID, spans, err := s.GetSpanTree(anchor)
	if err != nil {
		t.Fatalf("GetSpanTree: %v", err)
	}
	if gotTraceID != hex.EncodeToString(traceID) {
		t.Errorf("traceID=%q, want %q", gotTraceID, hex.EncodeToString(traceID))
	}
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(spans))
	}
	spanIDs := map[string]bool{}
	for _, sp := range spans {
		spanIDs[sp.SpanID] = true
	}
	if !spanIDs[hex.EncodeToString(anchor)] {
		t.Error("anchor span missing from result")
	}
	if !spanIDs[hex.EncodeToString(child)] {
		t.Error("child span missing from result")
	}
}

func TestGetSpanTree_CrossService(t *testing.T) {
	s := newStore(t)
	traceID := tid(0xDD)
	anchorID := sid(0x0A)
	peerID := sid(0x0B)

	if err := s.WriteResourceSpans(makeRS("frontend", "http-get", traceID, anchorID, nil, now, now.Add(time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write frontend span: %v", err)
	}
	if err := s.WriteResourceSpans(makeRS("backend", "db-query", traceID, peerID, anchorID, now.Add(10*time.Second), now.Add(11*time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write backend span: %v", err)
	}

	_, spans, err := s.GetSpanTree(anchorID)
	if err != nil {
		t.Fatalf("GetSpanTree: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2 (cross-service)", len(spans))
	}
}

func TestGetSpanTree_OutsideWindow(t *testing.T) {
	s := newStore(t)
	traceID := tid(0xEE)
	anchorID := sid(0x10)
	lateID := sid(0x11)
	earlyID := sid(0x12)

	if err := s.WriteResourceSpans(makeRS("svc", "root", traceID, anchorID, nil, now, now.Add(time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write anchor: %v", err)
	}
	// 3 minutes after anchor — outside the ±2-min window.
	if err := s.WriteResourceSpans(makeRS("svc", "late", traceID, lateID, nil, now.Add(3*time.Minute), now.Add(3*time.Minute+time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write late span: %v", err)
	}
	// 3 minutes before anchor — outside the ±2-min window.
	if err := s.WriteResourceSpans(makeRS("svc", "early", traceID, earlyID, nil, now.Add(-3*time.Minute), now.Add(-3*time.Minute+time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write early span: %v", err)
	}

	_, spans, err := s.GetSpanTree(anchorID)
	if err != nil {
		t.Fatalf("GetSpanTree: %v", err)
	}
	// Only the anchor itself should be returned (late and early are outside the window).
	if len(spans) != 1 {
		t.Errorf("got %d spans, want 1 (only anchor within window)", len(spans))
	}
}

func TestGetSpanTree_LookupByChildSpanID(t *testing.T) {
	s := newStore(t)
	traceID := tid(0xF0)
	rootID := sid(0x20)
	leafID := sid(0x21)

	if err := s.WriteResourceSpans(makeRS("svc", "root", traceID, rootID, nil, now, now.Add(2*time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write root: %v", err)
	}
	if err := s.WriteResourceSpans(makeRS("svc", "leaf", traceID, leafID, rootID, now.Add(time.Second), now.Add(2*time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write leaf: %v", err)
	}

	// Query by the leaf span ID — should return the whole tree.
	_, spans, err := s.GetSpanTree(leafID)
	if err != nil {
		t.Fatalf("GetSpanTree via leaf: %v", err)
	}
	if len(spans) != 2 {
		t.Errorf("got %d spans via leaf lookup, want 2", len(spans))
	}
}

// ── Cleanup ───────────────────────────────────────────────────────────────────

func TestCleanup_RemovesOldBuckets(t *testing.T) {
	s := newStore(t)
	old := time.Date(2020, 1, 15, 12, 0, 0, 0, time.UTC)
	rs := makeRS("old-svc", "op", tid(1), sid(1), nil, old, old.Add(time.Second), tracev1.Status_STATUS_CODE_OK)
	if err := s.WriteResourceSpans(rs); err != nil {
		t.Fatalf("WriteResourceSpans: %v", err)
	}

	// Verify bucket exists before cleanup.
	spans, _ := s.GetSpans("old-svc", 10)
	if len(spans) != 1 {
		t.Fatalf("expected 1 span before cleanup, got %d", len(spans))
	}

	if err := s.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	spans, _ = s.GetSpans("old-svc", 10)
	if len(spans) != 0 {
		t.Errorf("expected 0 spans after cleanup, got %d", len(spans))
	}
}

func TestCleanup_KeepsRecentBuckets(t *testing.T) {
	s := newStore(t)
	rs := makeRS("new-svc", "op", tid(1), sid(1), nil, now, now.Add(time.Second), tracev1.Status_STATUS_CODE_OK)
	if err := s.WriteResourceSpans(rs); err != nil {
		t.Fatalf("WriteResourceSpans: %v", err)
	}

	if err := s.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	spans, _ := s.GetSpans("new-svc", 10)
	if len(spans) != 1 {
		t.Errorf("got %d spans after cleanup, want 1 (recent bucket should be kept)", len(spans))
	}
}

func TestCleanup_RemovesEmptyServiceBucket(t *testing.T) {
	s := newStore(t)
	old := time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC)
	// Write spans only in an old month for this service.
	rs := makeRS("dead-svc", "op", tid(1), sid(1), nil, old, old.Add(time.Second), tracev1.Status_STATUS_CODE_OK)
	if err := s.WriteResourceSpans(rs); err != nil {
		t.Fatalf("WriteResourceSpans: %v", err)
	}

	if err := s.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// After cleanup the service bucket itself should be gone.
	infos, err := s.BucketStats()
	if err != nil {
		t.Fatalf("BucketStats: %v", err)
	}
	for _, info := range infos {
		if info.Name == "dead-svc" {
			t.Error("dead-svc service bucket should have been removed after cleanup")
		}
	}
}

func TestCleanup_MixedMonths(t *testing.T) {
	s := newStore(t)
	old := time.Date(2020, 3, 15, 12, 0, 0, 0, time.UTC)

	if err := s.WriteResourceSpans(makeRS("mix-svc", "old-op", tid(1), sid(1), nil, old, old.Add(time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write old: %v", err)
	}
	if err := s.WriteResourceSpans(makeRS("mix-svc", "new-op", tid(2), sid(2), nil, now, now.Add(time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write new: %v", err)
	}

	if err := s.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	spans, _ := s.GetSpans("mix-svc", 10)
	if len(spans) != 1 {
		t.Errorf("got %d spans, want 1 (only recent month kept)", len(spans))
	}
	if len(spans) == 1 && spans[0].Name != "new-op" {
		t.Errorf("remaining span name=%q, want new-op", spans[0].Name)
	}
}

// ── makeKey ───────────────────────────────────────────────────────────────────

func TestMakeKey_Length(t *testing.T) {
	key, err := makeKey(now, sid(0x42))
	if err != nil {
		t.Fatalf("makeKey: %v", err)
	}
	if len(key) != 24 {
		t.Errorf("key length=%d, want 24", len(key))
	}
}

func TestMakeKey_SpanIDSuffix(t *testing.T) {
	spanID := sid(0x37)
	key, err := makeKey(now, spanID)
	if err != nil {
		t.Fatalf("makeKey: %v", err)
	}
	if !bytes.Equal(key[16:], spanID) {
		t.Errorf("key suffix=%x, want %x", key[16:], spanID)
	}
}

func TestMakeKey_ShortSpanID(t *testing.T) {
	short := []byte{0x01, 0x02} // fewer than 8 bytes
	key, err := makeKey(now, short)
	if err != nil {
		t.Fatalf("makeKey: %v", err)
	}
	if len(key) != 24 {
		t.Errorf("key length=%d, want 24 even for short span ID", len(key))
	}
	// First 2 bytes of suffix match; rest are zero-padded.
	if key[16] != 0x01 || key[17] != 0x02 {
		t.Errorf("key suffix prefix=%x, want [01 02]", key[16:18])
	}
	for _, b := range key[18:] {
		if b != 0 {
			t.Errorf("expected zero padding in key suffix, got %x", key[18:])
			break
		}
	}
}

func TestMakeKey_Uniqueness(t *testing.T) {
	// Two keys for the same time/span should differ due to ULID random bits.
	k1, _ := makeKey(now, sid(1))
	k2, _ := makeKey(now, sid(1))
	if bytes.Equal(k1, k2) {
		t.Error("expected two keys for same time to differ (random ULID bits)")
	}
}

// ── timeBoundKey ─────────────────────────────────────────────────────────────

func TestTimeBoundKey_Length(t *testing.T) {
	k := timeBoundKey(now, 0x00)
	if len(k) != 24 {
		t.Errorf("timeBoundKey length=%d, want 24", len(k))
	}
}

func TestTimeBoundKey_TimestampEncoding(t *testing.T) {
	ms := ulid.Timestamp(now)
	lo := timeBoundKey(now, 0x00)

	// First 6 bytes encode the millisecond timestamp big-endian.
	gotMS := uint64(lo[0])<<40 | uint64(lo[1])<<32 | uint64(lo[2])<<24 |
		uint64(lo[3])<<16 | uint64(lo[4])<<8 | uint64(lo[5])
	if gotMS != ms {
		t.Errorf("encoded ms=%d, want %d", gotMS, ms)
	}
}

func TestTimeBoundKey_FillBytes(t *testing.T) {
	lo := timeBoundKey(now, 0x00)
	hi := timeBoundKey(now, 0xFF)
	for i := 6; i < 24; i++ {
		if lo[i] != 0x00 {
			t.Errorf("lo[%d]=%02x, want 0x00", i, lo[i])
		}
		if hi[i] != 0xFF {
			t.Errorf("hi[%d]=%02x, want 0xFF", i, hi[i])
		}
	}
}

func TestTimeBoundKey_Ordering(t *testing.T) {
	lo := timeBoundKey(now, 0x00)
	hi := timeBoundKey(now, 0xFF)
	if bytes.Compare(lo, hi) >= 0 {
		t.Error("expected lo < hi for the same timestamp")
	}

	// Keys for earlier time < keys for later time.
	earlier := timeBoundKey(now.Add(-time.Minute), 0xFF)
	later := timeBoundKey(now.Add(time.Minute), 0x00)
	if bytes.Compare(earlier, later) >= 0 {
		t.Error("expected earlier-time hi < later-time lo")
	}
}

// ── monthsInRange ─────────────────────────────────────────────────────────────

func TestMonthsInRange(t *testing.T) {
	tests := []struct {
		name     string
		from, to time.Time
		want     []string
	}{
		{
			name: "same month",
			from: time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
			to:   time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
			want: []string{"2024-03"},
		},
		{
			name: "two consecutive months",
			from: time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC),
			to:   time.Date(2024, 4, 15, 0, 0, 0, 0, time.UTC),
			want: []string{"2024-03", "2024-04"},
		},
		{
			name: "year boundary",
			from: time.Date(2023, 12, 15, 0, 0, 0, 0, time.UTC),
			to:   time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
			want: []string{"2023-12", "2024-01"},
		},
		{
			name: "three months",
			from: time.Date(2024, 1, 31, 23, 59, 59, 0, time.UTC),
			to:   time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
			want: []string{"2024-01", "2024-02", "2024-03"},
		},
		{
			name: "same instant",
			from: time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC),
			to:   time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC),
			want: []string{"2024-06"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := monthsInRange(tc.from, tc.to)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("monthsInRange = %v, want %v", got, tc.want)
			}
		})
	}
}

// ── compress / decompress ────────────────────────────────────────────────────

func TestCompressDecompress_Roundtrip(t *testing.T) {
	inputs := [][]byte{
		[]byte("hello, world"),
		bytes.Repeat([]byte{0x42}, 1024),
		[]byte{},
		[]byte{0x00, 0xFF, 0xAB, 0xCD},
	}
	for _, in := range inputs {
		compressed, err := compress(in)
		if err != nil {
			t.Fatalf("compress: %v", err)
		}
		got, err := decompress(compressed)
		if err != nil {
			t.Fatalf("decompress: %v", err)
		}
		if !bytes.Equal(got, in) {
			t.Errorf("roundtrip failed: got %x, want %x", got, in)
		}
	}
}

func TestDecompress_CorruptData(t *testing.T) {
	_, err := decompress([]byte{0x00, 0x01, 0x02, 0x03})
	if err == nil {
		t.Error("expected error for corrupt compressed data")
	}
}

// ── serviceName ───────────────────────────────────────────────────────────────

func TestServiceName(t *testing.T) {
	tests := []struct {
		name string
		rs   *tracev1.ResourceSpans
		want string
	}{
		{
			name: "service.name present",
			rs: &tracev1.ResourceSpans{
				Resource: &resourcev1.Resource{
					Attributes: []*commonv1.KeyValue{{
						Key:   "service.name",
						Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "my-service"}},
					}},
				},
			},
			want: "my-service",
		},
		{
			name: "no service.name attribute",
			rs: &tracev1.ResourceSpans{
				Resource: &resourcev1.Resource{
					Attributes: []*commonv1.KeyValue{{
						Key:   "other.attr",
						Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "value"}},
					}},
				},
			},
			want: "unknown",
		},
		{
			name: "nil resource",
			rs:   &tracev1.ResourceSpans{Resource: nil},
			want: "unknown",
		},
		{
			name: "empty attributes",
			rs:   &tracev1.ResourceSpans{Resource: &resourcev1.Resource{}},
			want: "unknown",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := serviceName(tc.rs)
			if got != tc.want {
				t.Errorf("serviceName=%q, want %q", got, tc.want)
			}
		})
	}
}

// ── spanStartTime ─────────────────────────────────────────────────────────────

func TestSpanStartTime_Valid(t *testing.T) {
	want := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	span := &tracev1.Span{StartTimeUnixNano: uint64(want.UnixNano())}
	got := spanStartTime(span)
	if !got.Equal(want) {
		t.Errorf("spanStartTime=%v, want %v", got, want)
	}
}

func TestSpanStartTime_ZeroFallsBackToNow(t *testing.T) {
	span := &tracev1.Span{StartTimeUnixNano: 0}
	before := time.Now().Add(-time.Second)
	got := spanStartTime(span)
	after := time.Now().Add(time.Second)
	if got.Before(before) || got.After(after) {
		t.Errorf("spanStartTime with zero nano = %v, expected approximately time.Now()", got)
	}
}

// ── buildSummary ──────────────────────────────────────────────────────────────

func TestBuildSummary(t *testing.T) {
	start := time.Date(2026, 2, 19, 12, 0, 0, 0, time.UTC)
	end := start.Add(500 * time.Millisecond)
	span := &tracev1.Span{
		TraceId:           tid(0xAB),
		SpanId:            sid(0xCD),
		ParentSpanId:      sid(0x01),
		Name:              "test-op",
		StartTimeUnixNano: uint64(start.UnixNano()),
		EndTimeUnixNano:   uint64(end.UnixNano()),
		Status:            &tracev1.Status{Code: tracev1.Status_STATUS_CODE_ERROR},
	}
	sum := buildSummary(span, "2026-02")

	if sum.TraceID != hex.EncodeToString(tid(0xAB)) {
		t.Errorf("TraceID=%q", sum.TraceID)
	}
	if sum.SpanID != hex.EncodeToString(sid(0xCD)) {
		t.Errorf("SpanID=%q", sum.SpanID)
	}
	if sum.ParentSpanID != hex.EncodeToString(sid(0x01)) {
		t.Errorf("ParentSpanID=%q", sum.ParentSpanID)
	}
	if sum.Name != "test-op" {
		t.Errorf("Name=%q", sum.Name)
	}
	if sum.Month != "2026-02" {
		t.Errorf("Month=%q", sum.Month)
	}
	if !sum.StartTime.Equal(start) {
		t.Errorf("StartTime=%v, want %v", sum.StartTime, start)
	}
	if !sum.EndTime.Equal(end) {
		t.Errorf("EndTime=%v, want %v", sum.EndTime, end)
	}
	if sum.Status != int32(tracev1.Status_STATUS_CODE_ERROR) {
		t.Errorf("Status=%d, want ERROR(%d)", sum.Status, tracev1.Status_STATUS_CODE_ERROR)
	}
}

// ── WriteResourceSpans edge cases ─────────────────────────────────────────────

func TestWrite_NilStatus(t *testing.T) {
	s := newStore(t)
	rs := &tracev1.ResourceSpans{
		Resource: &resourcev1.Resource{
			Attributes: []*commonv1.KeyValue{{
				Key:   "service.name",
				Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "nil-status-svc"}},
			}},
		},
		ScopeSpans: []*tracev1.ScopeSpans{{
			Spans: []*tracev1.Span{{
				TraceId:           tid(1),
				SpanId:            sid(1),
				Status:            nil, // nil Status — GetCode() must handle this gracefully
				StartTimeUnixNano: uint64(now.UnixNano()),
				EndTimeUnixNano:   uint64(now.Add(time.Second).UnixNano()),
			}},
		}},
	}
	if err := s.WriteResourceSpans(rs); err != nil {
		t.Fatalf("WriteResourceSpans with nil Status: %v", err)
	}
	spans, err := s.GetSpans("nil-status-svc", 10)
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Status != 0 {
		t.Errorf("status=%d, want 0 (UNSET) when Status is nil", spans[0].Status)
	}
}

func TestWrite_EmptyScopeSpans(t *testing.T) {
	s := newStore(t)
	rs := &tracev1.ResourceSpans{
		Resource: &resourcev1.Resource{
			Attributes: []*commonv1.KeyValue{{
				Key:   "service.name",
				Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "empty-scope-svc"}},
			}},
		},
		ScopeSpans: nil, // no spans at all
	}
	if err := s.WriteResourceSpans(rs); err != nil {
		t.Fatalf("WriteResourceSpans with nil ScopeSpans: %v", err)
	}
	spans, err := s.GetSpans("empty-scope-svc", 10)
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if len(spans) != 0 {
		t.Errorf("got %d spans, want 0 (no ScopeSpans means nothing stored)", len(spans))
	}
}

// ── GetSpans edge cases ────────────────────────────────────────────────────────

func TestGetSpans_LimitBreaksAcrossMonths(t *testing.T) {
	s := newStore(t)
	jan := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	feb := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)

	// Write 5 spans in each month.  Newest-first ordering picks Feb before Jan.
	for i := range 5 {
		s.WriteResourceSpans(makeRS("mm-lim-svc", "jan-op", tid(byte(i)), sid(byte(i)), nil, //nolint:errcheck
			jan.Add(time.Duration(i)*time.Second), jan.Add(time.Duration(i+1)*time.Second), tracev1.Status_STATUS_CODE_OK))
		s.WriteResourceSpans(makeRS("mm-lim-svc", "feb-op", tid(byte(i+10)), sid(byte(i+10)), nil, //nolint:errcheck
			feb.Add(time.Duration(i)*time.Second), feb.Add(time.Duration(i+1)*time.Second), tracev1.Status_STATUS_CODE_OK))
	}

	// limit=3 < 5 per month.  After filling from Feb the outer loop breaks before
	// touching Jan, exercising the "len(result) >= limit → break" branch.
	spans, err := s.GetSpans("mm-lim-svc", 3)
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	if len(spans) != 3 {
		t.Errorf("got %d spans, want 3", len(spans))
	}
	for _, sp := range spans {
		if sp.Month != "2026-02" {
			t.Errorf("span month=%q, want 2026-02 (newest month only; Jan should be skipped)", sp.Month)
		}
	}
}

func TestGetSpans_SkipsCorruptEntries(t *testing.T) {
	s := newStore(t)
	// Inject a corrupt entry directly into the bolt DB (all-zero key, garbage value).
	if err := s.db.Update(func(tx *bolt.Tx) error {
		outer, err := tx.CreateBucketIfNotExists([]byte("bad-svc"))
		if err != nil {
			return err
		}
		inner, err := outer.CreateBucketIfNotExists([]byte("2026-02"))
		if err != nil {
			return err
		}
		return inner.Put(make([]byte, 24), []byte("corrupt not valid data"))
	}); err != nil {
		t.Fatalf("inject corrupt entry: %v", err)
	}

	// Write a valid span in the same service+month.
	if err := s.WriteResourceSpans(makeRS("bad-svc", "valid-op", tid(1), sid(1), nil, now, now.Add(time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("WriteResourceSpans: %v", err)
	}

	spans, err := s.GetSpans("bad-svc", 10)
	if err != nil {
		t.Fatalf("GetSpans: %v", err)
	}
	// Corrupt entry must be skipped; only the valid span is returned.
	if len(spans) != 1 {
		t.Errorf("got %d spans, want 1 (corrupt entry should be skipped)", len(spans))
	}
}

// ── BucketStats edge cases ────────────────────────────────────────────────────

func TestBucketStats_MultipleMonths(t *testing.T) {
	s := newStore(t)
	jan := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	feb := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)

	s.WriteResourceSpans(makeRS("mm-stats-svc", "op", tid(1), sid(1), nil, jan, jan.Add(time.Second), tracev1.Status_STATUS_CODE_OK))   //nolint:errcheck
	s.WriteResourceSpans(makeRS("mm-stats-svc", "op", tid(2), sid(2), nil, feb, feb.Add(time.Second), tracev1.Status_STATUS_CODE_OK))   //nolint:errcheck
	s.WriteResourceSpans(makeRS("mm-stats-svc", "op", tid(3), sid(3), nil, feb.Add(time.Second), feb.Add(2*time.Second), tracev1.Status_STATUS_CODE_OK)) //nolint:errcheck

	infos, err := s.BucketStats()
	if err != nil {
		t.Fatalf("BucketStats: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("got %d services, want 1", len(infos))
	}
	if len(infos[0].Months) != 2 {
		t.Fatalf("got %d months, want 2", len(infos[0].Months))
	}
	counts := map[string]int64{}
	for _, m := range infos[0].Months {
		counts[m.Month] = m.SpanCount
	}
	if counts["2026-01"] != 1 {
		t.Errorf("2026-01 span count=%d, want 1", counts["2026-01"])
	}
	if counts["2026-02"] != 2 {
		t.Errorf("2026-02 span count=%d, want 2", counts["2026-02"])
	}
}

// ── findSpanByID edge cases ───────────────────────────────────────────────────

func TestFindSpanByID_SkipsCorruptEntry(t *testing.T) {
	s := newStore(t)
	targetSpanID := sid(0x99)

	// Insert corrupt data whose key suffix matches targetSpanID.
	if err := s.db.Update(func(tx *bolt.Tx) error {
		outer, err := tx.CreateBucketIfNotExists([]byte("find-svc"))
		if err != nil {
			return err
		}
		inner, err := outer.CreateBucketIfNotExists([]byte("2026-02"))
		if err != nil {
			return err
		}
		key := make([]byte, 24)
		copy(key[16:], targetSpanID)
		return inner.Put(key, []byte("not valid compressed data"))
	}); err != nil {
		t.Fatalf("inject corrupt entry: %v", err)
	}

	// findSpanByID should skip the corrupt entry and return nil (not found).
	result, err := s.findSpanByID(targetSpanID)
	if err != nil {
		t.Fatalf("findSpanByID: %v", err)
	}
	if result != nil {
		t.Error("expected nil from findSpanByID when entry is corrupt, got non-nil")
	}
}

// ── decodeSpan error paths ────────────────────────────────────────────────────

func TestDecodeSpan_InvalidProto(t *testing.T) {
	// Byte 0x00 encodes field number 0 (invalid in proto3).
	// proto.Unmarshal must reject it.
	compressed, err := compress([]byte{0x00})
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	_, err = decodeSpan(compressed)
	if err == nil {
		t.Error("expected error for invalid proto wire format, got nil")
	}
}

func TestDecodeSpan_NoSpanInEntry(t *testing.T) {
	// A valid proto encoding of an empty ResourceSpans (no ScopeSpans) must
	// trigger the "no span in entry" error.
	data, err := proto.Marshal(&tracev1.ResourceSpans{})
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	compressed, err := compress(data)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	_, err = decodeSpan(compressed)
	if err == nil {
		t.Error("expected 'no span in entry' error for empty ResourceSpans, got nil")
	}
}

// ── Cleanup edge cases ────────────────────────────────────────────────────────

func TestCleanup_InvalidMonthFormat(t *testing.T) {
	s := newStore(t)
	// Create a sub-bucket whose key is not a valid YYYY-MM timestamp.
	if err := s.db.Update(func(tx *bolt.Tx) error {
		outer, err := tx.CreateBucketIfNotExists([]byte("inv-svc"))
		if err != nil {
			return err
		}
		_, err = outer.CreateBucketIfNotExists([]byte("not-a-month"))
		return err
	}); err != nil {
		t.Fatalf("create invalid bucket: %v", err)
	}

	// Cleanup must not error on unparseable month keys.
	if err := s.Cleanup(); err != nil {
		t.Fatalf("Cleanup returned unexpected error: %v", err)
	}

	// The malformed bucket must still exist (it was skipped, not deleted).
	infos, err := s.BucketStats()
	if err != nil {
		t.Fatalf("BucketStats: %v", err)
	}
	found := false
	for _, info := range infos {
		if info.Name == "inv-svc" {
			for _, m := range info.Months {
				if m.Month == "not-a-month" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("'not-a-month' bucket should remain after Cleanup (invalid format is skipped)")
	}
}

// ── GetSpanTree window boundary ───────────────────────────────────────────────

func TestGetSpanTree_AtWindowBoundary(t *testing.T) {
	s := newStore(t)
	traceID := tid(0xA0)
	anchorID := sid(0x30)
	atPlus2 := sid(0x31)
	atMinus2 := sid(0x32)

	if err := s.WriteResourceSpans(makeRS("bnd-svc", "root", traceID, anchorID, nil,
		now, now.Add(time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write anchor: %v", err)
	}
	// Exactly at the +2-minute boundary — should be included.
	plus2 := now.Add(2 * time.Minute)
	if err := s.WriteResourceSpans(makeRS("bnd-svc", "+2min", traceID, atPlus2, nil,
		plus2, plus2.Add(time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write +2min: %v", err)
	}
	// Exactly at the -2-minute boundary — should be included.
	minus2 := now.Add(-2 * time.Minute)
	if err := s.WriteResourceSpans(makeRS("bnd-svc", "-2min", traceID, atMinus2, nil,
		minus2, minus2.Add(time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write -2min: %v", err)
	}

	_, spans, err := s.GetSpanTree(anchorID)
	if err != nil {
		t.Fatalf("GetSpanTree: %v", err)
	}
	if len(spans) != 3 {
		t.Errorf("got %d spans, want 3 (boundary spans are included in the ±2-min window)", len(spans))
	}
}

func TestGetSpanTree_DBError(t *testing.T) {
	// Close the store before calling GetSpanTree to force the findSpanByID
	// db.View to fail, exercising the "return "", nil, err" branch.
	s, err := Open(filepath.Join(t.TempDir(), "test.db"), 60)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Close()
	_, _, err = s.GetSpanTree(sid(0x99))
	if err == nil {
		t.Error("expected error from GetSpanTree after store closed, got nil")
	}
}

// ── findSpanByID multi-month early exit ───────────────────────────────────────

func TestFindSpanByID_MultipleMonths_EarlyExit(t *testing.T) {
	s := newStore(t)
	jan := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	feb := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	targetID := sid(0x55)

	// Span in Jan (lexicographically earlier bucket "2026-01").
	// Service also has a Feb bucket ("2026-02") so ForEach iterates both.
	// After finding the span in Jan the ForEach callback for Feb fires with
	// result != nil, exercising the per-month early-exit path.
	if err := s.WriteResourceSpans(makeRS("mm-find-svc", "jan-span", tid(1), targetID, nil,
		jan, jan.Add(time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write jan: %v", err)
	}
	if err := s.WriteResourceSpans(makeRS("mm-find-svc", "feb-span", tid(2), sid(0x56), nil,
		feb, feb.Add(time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write feb: %v", err)
	}

	result, err := s.findSpanByID(targetID)
	if err != nil {
		t.Fatalf("findSpanByID: %v", err)
	}
	if result == nil {
		t.Fatal("expected span to be found, got nil")
	}
	if result.Name != "jan-span" {
		t.Errorf("found name=%q, want jan-span", result.Name)
	}
}

// ── scanWindow corrupt-entry skip ─────────────────────────────────────────────

func TestScanWindow_SkipsCorruptEntries(t *testing.T) {
	s := newStore(t)
	traceID := tid(0xC0)
	anchorID := sid(0x60)
	childID := sid(0x61)

	if err := s.WriteResourceSpans(makeRS("sw-svc", "root", traceID, anchorID, nil,
		now, now.Add(time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write anchor: %v", err)
	}
	if err := s.WriteResourceSpans(makeRS("sw-svc", "child", traceID, childID, anchorID,
		now.Add(30*time.Second), now.Add(31*time.Second), tracev1.Status_STATUS_CODE_OK)); err != nil {
		t.Fatalf("write child: %v", err)
	}

	// Inject a corrupt entry whose key falls inside the ±2-minute scan window.
	corruptKey := timeBoundKey(now.Add(time.Minute), 0x80) // well inside the window
	if err := s.db.Update(func(tx *bolt.Tx) error {
		outer := tx.Bucket([]byte("sw-svc"))
		inner := outer.Bucket([]byte(now.Format("2006-01")))
		return inner.Put(corruptKey, []byte("corrupt not valid data"))
	}); err != nil {
		t.Fatalf("inject corrupt entry: %v", err)
	}

	_, spans, err := s.GetSpanTree(anchorID)
	if err != nil {
		t.Fatalf("GetSpanTree: %v", err)
	}
	// Corrupt entry is skipped; both valid spans are still returned.
	if len(spans) != 2 {
		t.Errorf("got %d spans, want 2 (corrupt entry in window should be skipped)", len(spans))
	}
}
