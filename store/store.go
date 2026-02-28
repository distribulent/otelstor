package store

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/oklog/ulid/v2"
	bolt "go.etcd.io/bbolt"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

const DefaultRetentionDays = 60

// Store wraps a bbolt database for trace storage.
type Store struct {
	db            *bolt.DB
	retentionDays int
}

// Open opens or creates the bbolt database at the given path.
// retentionDays controls how long trace data is kept; 0 uses the default (60 days).
func Open(path string, retentionDays int) (*Store, error) {
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return nil, err
	}
	if retentionDays <= 0 {
		retentionDays = DefaultRetentionDays
	}
	return &Store{db: db, retentionDays: retentionDays}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// WriteResourceSpans writes each span in rs as a separate DB entry.
// Bucket hierarchy: service-name -> YYYY-MM -> (ULID || SpanId) key -> compressed protobuf.
// The key is 24 bytes: 16-byte ULID of the span start time followed by the 8-byte span_id.
func (s *Store) WriteResourceSpans(rs *tracev1.ResourceSpans) error {
	svc := serviceName(rs)

	return s.db.Update(func(tx *bolt.Tx) error {
		outer, err := tx.CreateBucketIfNotExists([]byte(svc))
		if err != nil {
			return err
		}

		for _, sls := range rs.ScopeSpans {
			for _, span := range sls.Spans {
				t := spanStartTime(span)

				inner, err := outer.CreateBucketIfNotExists([]byte(t.Format("2006-01")))
				if err != nil {
					return err
				}

				key, err := makeKey(t, span.SpanId)
				if err != nil {
					return fmt.Errorf("key: %w", err)
				}

				// Wrap the single span in a ResourceSpans to preserve resource and scope context.
				entry := &tracev1.ResourceSpans{
					Resource:  rs.Resource,
					SchemaUrl: rs.SchemaUrl,
					ScopeSpans: []*tracev1.ScopeSpans{{
						Scope:     sls.Scope,
						SchemaUrl: sls.SchemaUrl,
						Spans:     []*tracev1.Span{span},
					}},
				}

				data, err := proto.Marshal(entry)
				if err != nil {
					return fmt.Errorf("marshal: %w", err)
				}
				compressed, err := compress(data)
				if err != nil {
					return fmt.Errorf("compress: %w", err)
				}

				if err := inner.Put(key, compressed); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// makeKey returns a 24-byte key: 16-byte ULID of t followed by the 8-byte spanID.
// spanID is zero-padded if shorter than 8 bytes.
func makeKey(t time.Time, spanID []byte) ([]byte, error) {
	id, err := ulid.New(ulid.Timestamp(t), rand.Reader)
	if err != nil {
		return nil, err
	}
	ulidBytes, _ := id.MarshalBinary() // always 16 bytes
	var sid [8]byte
	copy(sid[:], spanID)
	return append(ulidBytes, sid[:]...), nil
}

// MonthInfo holds stats for one month sub-bucket.
type MonthInfo struct {
	Month     string
	SpanCount int64
}

// ServiceInfo holds stats for one service bucket and its month sub-buckets.
type ServiceInfo struct {
	Name   string
	Months []MonthInfo
}

// BucketStats returns the list of service buckets and their month sub-buckets
// together with the span count for each month.
func (s *Store) BucketStats() ([]ServiceInfo, error) {
	var result []ServiceInfo
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(svcName []byte, outer *bolt.Bucket) error {
			info := ServiceInfo{Name: string(svcName)}
			outer.ForEach(func(monthKey, v []byte) error { //nolint:errcheck
				if v != nil {
					return nil // not a nested bucket
				}
				inner := outer.Bucket(monthKey)
				var count int64
				if inner != nil {
					count = int64(inner.Stats().KeyN)
				}
				info.Months = append(info.Months, MonthInfo{
					Month:     string(monthKey),
					SpanCount: count,
				})
				return nil
			})
			result = append(result, info)
			return nil
		})
	})
	return result, err
}

// SpanSummary holds the key fields of a stored span for display purposes.
type SpanSummary struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Name         string
	Month        string
	StartTime    time.Time
	EndTime      time.Time
	Status       int32
	SpanProto    []byte // serialized opentelemetry.proto.trace.v1.Span
}

func buildSummary(span *tracev1.Span, month string) SpanSummary {
	spanBytes, _ := proto.Marshal(span)
	return SpanSummary{
		TraceID:      hex.EncodeToString(span.TraceId),
		SpanID:       hex.EncodeToString(span.SpanId),
		ParentSpanID: hex.EncodeToString(span.ParentSpanId),
		Name:         span.Name,
		Month:        month,
		StartTime:    time.Unix(0, int64(span.StartTimeUnixNano)).UTC(),
		EndTime:      time.Unix(0, int64(span.EndTimeUnixNano)).UTC(),
		Status:       int32(span.Status.GetCode()),
		SpanProto:    spanBytes,
	}
}

// DeleteService removes the top-level bucket for the named service and all of
// its data. It is a no-op if the service does not exist.
func (s *Store) DeleteService(service string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte(service)) == nil {
			return nil
		}
		return tx.DeleteBucket([]byte(service))
	})
}

// GetSpans returns the last `limit` spans for the named service, ordered most
// recent first. It walks month buckets in reverse chronological order and uses
// a reverse cursor within each bucket (keys are ULID-prefixed so they sort by
// time). If limit <= 0 it defaults to 50.
func (s *Store) GetSpans(service string, limit int) ([]SpanSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	var result []SpanSummary

	err := s.db.View(func(tx *bolt.Tx) error {
		outer := tx.Bucket([]byte(service))
		if outer == nil {
			return nil
		}

		// Collect month names and sort newest-first.
		var months []string
		outer.ForEach(func(k, v []byte) error { //nolint:errcheck
			if v == nil { // nested bucket
				months = append(months, string(k))
			}
			return nil
		})
		sort.Sort(sort.Reverse(sort.StringSlice(months)))

		for _, month := range months {
			if len(result) >= limit {
				break
			}
			inner := outer.Bucket([]byte(month))
			if inner == nil {
				continue
			}
			cur := inner.Cursor()
			for k, v := cur.Last(); k != nil && len(result) < limit; k, v = cur.Prev() {
				span, err := decodeSpan(v)
				if err != nil {
					continue
				}
				result = append(result, buildSummary(span, month))
			}
		}
		return nil
	})
	return result, err
}

// ServiceSummary holds aggregate statistics for a single service.
type ServiceSummary struct {
	Name        string
	TraceCount  int64
	SpanCount   int64
	LastUpdated time.Time
}

// ListServices returns one ServiceSummary per stored service. For each service it
// counts total spans and distinct trace IDs by scanning all month buckets, and
// extracts the exact timestamp of the most recent span from the ULID key prefix
// of the last entry in the newest month bucket.
func (s *Store) ListServices() ([]ServiceSummary, error) {
	var result []ServiceSummary

	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(svcName []byte, outer *bolt.Bucket) error {
			sum := ServiceSummary{Name: string(svcName)}
			seen := make(map[string]bool)

			var months []string
			outer.ForEach(func(k, v []byte) error { //nolint:errcheck
				if v == nil {
					months = append(months, string(k))
				}
				return nil
			})
			sort.Sort(sort.Reverse(sort.StringSlice(months)))

			// Extract last-updated time from the ULID prefix of the last key
			// in the most recent month bucket — no decompression needed.
			if len(months) > 0 {
				if inner := outer.Bucket([]byte(months[0])); inner != nil {
					if k, _ := inner.Cursor().Last(); len(k) >= 16 {
						var id ulid.ULID
						copy(id[:], k[:16])
						sum.LastUpdated = time.UnixMilli(int64(id.Time())).UTC()
					}
				}
			}

			for _, month := range months {
				inner := outer.Bucket([]byte(month))
				if inner == nil {
					continue
				}
				inner.ForEach(func(_, v []byte) error { //nolint:errcheck
					sum.SpanCount++
					span, err := decodeSpan(v)
					if err != nil {
						return nil
					}
					tid := hex.EncodeToString(span.TraceId)
					if !seen[tid] {
						seen[tid] = true
						sum.TraceCount++
					}
					return nil
				})
			}

			result = append(result, sum)
			return nil
		})
	})
	return result, err
}

// GetTraceIDs returns the last `limit` unique trace IDs for the named service,
// ordered most recent first. Scans spans newest-first across month buckets and
// deduplicates by trace ID. If limit <= 0 it defaults to 100.
func (s *Store) GetTraceIDs(service string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	seen := make(map[string]bool)
	var result []string

	err := s.db.View(func(tx *bolt.Tx) error {
		outer := tx.Bucket([]byte(service))
		if outer == nil {
			return nil
		}

		var months []string
		outer.ForEach(func(k, v []byte) error { //nolint:errcheck
			if v == nil {
				months = append(months, string(k))
			}
			return nil
		})
		sort.Sort(sort.Reverse(sort.StringSlice(months)))

		for _, month := range months {
			if len(result) >= limit {
				break
			}
			inner := outer.Bucket([]byte(month))
			if inner == nil {
				continue
			}
			cur := inner.Cursor()
			for k, v := cur.Last(); k != nil && len(result) < limit; k, v = cur.Prev() {
				span, err := decodeSpan(v)
				if err != nil {
					continue
				}
				tid := hex.EncodeToString(span.TraceId)
				if !seen[tid] {
					seen[tid] = true
					result = append(result, tid)
				}
			}
		}
		return nil
	})
	return result, err
}

// GetTraceByID returns all spans whose trace_id matches the given hex string,
// scanning every service and month bucket. This is a full table scan; use it
// for interactive trace lookups where completeness matters over speed.
func (s *Store) GetTraceByID(traceID string) ([]SpanSummary, error) {
	var result []SpanSummary
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(_ []byte, outer *bolt.Bucket) error {
			return outer.ForEach(func(monthKey, v []byte) error {
				if v != nil {
					return nil // not a sub-bucket
				}
				inner := outer.Bucket(monthKey)
				if inner == nil {
					return nil
				}
				return inner.ForEach(func(_, val []byte) error {
					span, err := decodeSpan(val)
					if err != nil {
						return nil
					}
					if hex.EncodeToString(span.TraceId) == traceID {
						result = append(result, buildSummary(span, string(monthKey)))
					}
					return nil
				})
			})
		})
	})
	return result, err
}

// GetSpanTree finds the span with the given span_id, then returns all spans
// that share its trace_id within a ±2-minute window around its start time.
// The span_id must be the raw 8-byte value (not hex). Returns an empty traceID
// and nil slice if the span is not found.
func (s *Store) GetSpanTree(spanID []byte) (traceID string, spans []SpanSummary, err error) {
	anchor, err := s.findSpanByID(spanID)
	if err != nil {
		return "", nil, err
	}
	if anchor == nil {
		return "", nil, nil
	}
	from := anchor.StartTime.Add(-2 * time.Minute)
	to   := anchor.StartTime.Add(2 * time.Minute)
	spans, err = s.scanWindow(anchor.TraceID, from, to)
	return anchor.TraceID, spans, err
}

// findSpanByID scans all buckets for a span whose key suffix matches spanID.
// Key comparison is done on raw keys (no decompression) for speed.
func (s *Store) findSpanByID(spanID []byte) (*SpanSummary, error) {
	var result *SpanSummary
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(_ []byte, outer *bolt.Bucket) error {
			if result != nil {
				return nil
			}
			return outer.ForEach(func(monthKey, v []byte) error {
				if result != nil || v != nil {
					return nil // already found, or not a sub-bucket
				}
				inner := outer.Bucket(monthKey)
				if inner == nil {
					return nil
				}
				cur := inner.Cursor()
				for k, val := cur.First(); k != nil; k, val = cur.Next() {
					if len(k) == 24 && bytes.Equal(k[16:], spanID) {
						sp, err := decodeSpan(val)
						if err != nil {
							continue
						}
						sum := buildSummary(sp, string(monthKey))
						result = &sum
						return nil
					}
				}
				return nil
			})
		})
	})
	return result, err
}

// scanWindow returns all spans whose trace_id matches traceID and whose ULID
// key falls within [from, to]. It scans every service bucket so it captures
// spans from all services that participated in the same trace.
func (s *Store) scanWindow(traceID string, from, to time.Time) ([]SpanSummary, error) {
	loKey := timeBoundKey(from, 0x00)
	hiKey := timeBoundKey(to, 0xFF)
	months := monthsInRange(from, to)

	var result []SpanSummary
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(_ []byte, outer *bolt.Bucket) error {
			for _, month := range months {
				inner := outer.Bucket([]byte(month))
				if inner == nil {
					continue
				}
				cur := inner.Cursor()
				for k, v := cur.Seek(loKey); k != nil && bytes.Compare(k, hiKey) <= 0; k, v = cur.Next() {
					sp, err := decodeSpan(v)
					if err != nil {
						continue
					}
					if hex.EncodeToString(sp.TraceId) != traceID {
						continue
					}
					result = append(result, buildSummary(sp, month))
				}
			}
			return nil
		})
	})
	return result, err
}

// timeBoundKey builds a 24-byte cursor bound for the given time.
// The 16-byte ULID portion has the timestamp set and the random bits filled
// with `fill` (0x00 for lower bound, 0xFF for upper bound).
// The 8-byte span_id suffix is also set to `fill`.
func timeBoundKey(t time.Time, fill byte) []byte {
	ms := ulid.Timestamp(t)
	key := make([]byte, 24)
	key[0] = byte(ms >> 40)
	key[1] = byte(ms >> 32)
	key[2] = byte(ms >> 24)
	key[3] = byte(ms >> 16)
	key[4] = byte(ms >> 8)
	key[5] = byte(ms)
	for i := 6; i < 24; i++ {
		key[i] = fill
	}
	return key
}

// monthsInRange returns the YYYY-MM strings for every month that overlaps [from, to].
func monthsInRange(from, to time.Time) []string {
	var months []string
	cur := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(to.Year(), to.Month(), 1, 0, 0, 0, 0, time.UTC)
	for !cur.After(end) {
		months = append(months, cur.Format("2006-01"))
		cur = cur.AddDate(0, 1, 0)
	}
	return months
}

// decodeSpan decompresses a stored entry and returns the single span inside it.
func decodeSpan(data []byte) (*tracev1.Span, error) {
	raw, err := decompress(data)
	if err != nil {
		return nil, err
	}
	var rs tracev1.ResourceSpans
	if err := proto.Unmarshal(raw, &rs); err != nil {
		return nil, err
	}
	for _, sls := range rs.ScopeSpans {
		for _, span := range sls.Spans {
			return span, nil
		}
	}
	return nil, fmt.Errorf("no span in entry")
}

func decompress(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// Cleanup removes month buckets older than the configured retention period,
// and removes any service bucket that becomes empty as a result.
func (s *Store) Cleanup() error {
	cutoff := time.Now().AddDate(0, 0, -s.retentionDays)

	return s.db.Update(func(tx *bolt.Tx) error {
		type pair struct{ svc, month string }
		var toDelete []pair

		tx.ForEach(func(svcName []byte, outer *bolt.Bucket) error { //nolint:errcheck
			return outer.ForEach(func(monthKey, v []byte) error {
				if v != nil {
					return nil // not a nested bucket
				}
				t, err := time.Parse("2006-01", string(monthKey))
				if err != nil {
					return nil
				}
				// End of the month boundary: first day of the next month.
				endOfMonth := time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
				if endOfMonth.Before(cutoff) {
					toDelete = append(toDelete, pair{string(svcName), string(monthKey)})
				}
				return nil
			})
		})

		for _, e := range toDelete {
			outer := tx.Bucket([]byte(e.svc))
			if outer == nil {
				continue
			}
			if err := outer.DeleteBucket([]byte(e.month)); err != nil {
				return fmt.Errorf("delete %s/%s: %w", e.svc, e.month, err)
			}
			// Remove the service bucket if it is now empty.
			empty := true
			outer.ForEach(func(k, v []byte) error { //nolint:errcheck
				empty = false
				return nil
			})
			if empty {
				if err := tx.DeleteBucket([]byte(e.svc)); err != nil {
					return fmt.Errorf("delete svc bucket %s: %w", e.svc, err)
				}
			}
		}
		return nil
	})
}

func serviceName(rs *tracev1.ResourceSpans) string {
	if rs.Resource == nil {
		return "unknown"
	}
	for _, attr := range rs.Resource.Attributes {
		if attr.Key == "service.name" {
			return attr.Value.GetStringValue()
		}
	}
	return "unknown"
}

func spanStartTime(span *tracev1.Span) time.Time {
	if span.StartTimeUnixNano > 0 {
		return time.Unix(0, int64(span.StartTimeUnixNano)).UTC()
	}
	return time.Now().UTC()
}

func compress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
