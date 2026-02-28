# Purpose: OpenTelemetry backend collector and storage

## Overview

Go-based OTLP gRPC server that receives traces from an OpenTelemetry collector and stores them locally in a bbolt database.

## API

### TraceService — gRPC (`opentelemetry.proto.collector.trace.v1`)

- Implements the OTLP gRPC `TraceService.Export` endpoint
- Port configurable via `port` in config; defaults to `4317` (standard OTLP gRPC port)
- Accepts `ExportTraceServiceRequest` containing one or more `ResourceSpans`

### TraceService — HTTP (`POST /v1/traces`)

- Standard OTLP HTTP/protobuf collector endpoint
- Port configurable via `http_port` in config; defaults to `4318` (standard OTLP HTTP port); set to `0` to disable
- `Content-Type: application/x-protobuf` — body is a binary-serialised `ExportTraceServiceRequest`
- Response is a binary-serialised `ExportTraceServiceResponse` with `Content-Type: application/x-protobuf`
- Feeds into the same `store.WriteResourceSpans` path as the gRPC endpoint

### StatsService — gRPC (`otelstor.StatsService` — `proto/stats.proto`)

- `GetStats(StatsRequest) → StatsResponse`
  - Returns the effective server configuration and a full listing of the bbolt bucket tree:
    - `config` — resolved `ServerConfig` (port, data_dir, retention_days with defaults applied)
    - `services` — list of `ServiceBucket`, one per service name, each containing:
      - `months` — list of `MonthBucket` (`month` as `YYYY-MM`, `span_count` of stored spans)

- `GetSpans(GetSpansRequest) → GetSpansResponse`
  - Returns up to `limit` stored spans for a named service (default 50), ordered most-recent first
  - Walks month buckets in reverse chronological order; within each bucket uses a reverse bbolt cursor so no full scan is required
  - Each `SpanEntry` contains: `trace_id`, `span_id`, `parent_span_id`, `name`, `month`, `start_time_unix_nano`, `end_time_unix_nano`, `status_code` (all IDs hex-encoded), and `span_proto` (serialized `opentelemetry.proto.trace.v1.Span` bytes — the full OTLP span including attributes, events, links, kind, and status message)

- `GetSpanTree(GetSpanTreeRequest) → GetSpanTreeResponse`
  - Accepts a `span_id` (hex); locates the span by scanning key suffixes across all buckets (no value decompression until matched)
  - Computes a ±2-minute window around the matched span's `StartTimeUnixNano`
  - Determines which `YYYY-MM` buckets overlap the window (`monthsInRange`)
  - Performs a ULID range scan across **all service buckets** for those months, filtering to spans sharing the same `trace_id` — this captures distributed spans from any service that participated in the trace
  - Returns `trace_id` and the full `[]SpanEntry` slice; clients reconstruct the tree using `parent_span_id` links

## Configuration

Loaded from a textproto file (default: `otelstor.cfg`). Override the path with the `-config` flag:

```
./otelstor -config /etc/otelstor/prod.cfg
```

If the default `otelstor.cfg` is absent the server starts with built-in defaults and logs a warning. An explicit `-config` path that does not exist is a fatal error.

**`proto/config.proto` — `ServerConfig` fields:**

| Field | Type | Default | Description |
|---|---|---|---|
| `port` | int32 | 4317 | gRPC listen port |
| `data_dir` | string | `"."` | Directory where `traces.db` is created |
| `retention_days` | int32 | 60 | Days to retain trace data before cleanup |
| `http_port` | int32 | 4318 | OTLP HTTP/protobuf port (`0` to disable) |

**Example `otelstor.cfg`:**
```
port: 4317
http_port: 4318
data_dir: "."
retention_days: 60
```

## Storage (`store/store.go`)

**Database:** `go.etcd.io/bbolt v1.4.3` — single file `traces.db` inside `data_dir`.

**Bucket hierarchy:** nested bbolt buckets organized by service and month:
```
service-name/
  YYYY-MM/
    <24-byte key> -> <zlib-compressed ResourceSpans protobuf (single span)>
```

- **Service name** extracted from `Resource.Attributes["service.name"]`; falls back to `"unknown"`
- **Bucket month** derived from each span's `StartTimeUnixNano`; falls back to `time.Now()`
- **Key** — 24 bytes: 16-byte binary ULID of the span start time (via `github.com/oklog/ulid/v2` with `crypto/rand` entropy) concatenated with the 8-byte `span_id` field. Keys sort chronologically and are unique per span.
- **Value** — zlib-compressed `ResourceSpans` protobuf containing a single span, with the original `Resource` and `Scope` context preserved
- Each span in an `ExportTraceServiceRequest` is written as its own entry

**Service listing:** `Store.ListServices()` scans all service buckets in one read transaction. For each service it: (a) reads the ULID timestamp prefix of the last key in the most recent month bucket to get the exact `LastUpdated` time without decompressing any values; (b) iterates all entries in all months, counting spans and collecting unique trace IDs via a `map[string]bool`. The ULID extraction is O(1) per service; the full scan is O(total spans) for the trace deduplication.

**Trace lookup by ID:** `Store.GetTraceByID(traceID string)` does a full table scan: visits every service bucket → every month bucket → every entry, decompresses each, and keeps those whose `TraceId` matches. O(total spans). Appropriate for interactive CLI use where completeness matters over speed. The existing `GetSpanTree` is faster for the common UI case (anchor span is known) because it uses the ULID time window to bound the scan.

**Reading trace IDs:** `Store.GetTraceIDs(service, limit)` returns the last `limit` unique trace IDs for a service. Uses the same newest-first month + reverse cursor traversal as `GetSpans`, but deduplicates by trace ID using an in-memory `map[string]bool`. Stops once `limit` unique IDs are collected, so it is efficient even when many spans share the same trace.

**Reading:** `Store.GetSpans(service, limit)` returns the most recent spans for a service. Month buckets are visited newest-first; within each bucket a reverse cursor (`Last` / `Prev`) is used so retrieval is O(limit) rather than O(total entries). Each raw entry is decompressed and unmarshalled to extract span fields.

**Span tree lookup:** `Store.GetSpanTree(spanID []byte)` first calls `findSpanByID` which iterates keys (not values) checking the 8-byte suffix; once the anchor span is found its timestamp and trace_id are used to drive `scanWindow`. `scanWindow` builds a 24-byte ULID range bound via `timeBoundKey` (6-byte ms timestamp + fill byte for random and span_id portions) and uses `Cursor.Seek` + forward iteration across all service/month bucket pairs in the window, filtering by `trace_id`.

**Cleanup:** `Store.Cleanup()` runs hourly. Deletes any month bucket whose end-of-month boundary is older than `retention_days`. Removes the parent service bucket if it becomes empty.

## Dependencies

| Module | Version | Purpose |
|---|---|---|
| `go.etcd.io/bbolt` | v1.4.3 | embedded key-value store |
| `github.com/oklog/ulid/v2` | v2.1.0 | time-ordered binary keys |
| `go.opentelemetry.io/proto/otlp` | v1.3.1 | OTLP protobuf types and gRPC service |
| `google.golang.org/grpc` | v1.78.0 | gRPC server |
| `google.golang.org/protobuf` | v1.36.11 | protobuf marshalling and textproto parsing |

## Dashboard (`cmd/dashboard/`)

A separate HTTP server that acts as a thin bridge between the browser and the otelstor gRPC server. It serves a Svelte UI and exposes a REST API over gRPC.

### Go bridge (`cmd/dashboard/main.go`)

Flags:

| Flag | Default | Description |
|---|---|---|
| `-grpc-addrs` | `localhost:4317` | Comma-separated list of otelstor gRPC server addresses |
| `-port` | `10731` | HTTP port for the dashboard |

Endpoints:
- `GET /api/backends` — returns the list of configured gRPC backend addresses as JSON array
- `GET /api/stats[&backend=<addr>]` — calls `StatsService.GetStats` over gRPC, returns JSON
- `GET /api/spans?service=<name>[&limit=<n>][&backend=<addr>]` — calls `StatsService.GetSpans` over gRPC; each span's `SpanProto` bytes are decoded via `proto.Unmarshal` + `protojson.Marshal` into a `span_details` JSON object containing the full OTLP span (kind, traceState, attributes, events, links, status message, dropped counts)
- `GET /api/spantree?span_id=<hex>[&backend=<addr>]` — calls `StatsService.GetSpanTree`; same `span_details` enrichment as `/api/spans`
- `DELETE /api/service?service=<name>[&backend=<addr>]` — calls `StatsService.DeleteService` over gRPC
- `GET /` (and all other paths) — serves the static Svelte build

The dashboard performs proto decoding server-side so the browser receives plain JSON and requires no protobuf library.

### Svelte UI (`cmd/dashboard/ui/`)

Plain Vite + Svelte 5 (no SvelteKit). During development Vite proxies `/api` to `:10731`; in production the Go bridge serves both on the same origin.

**Components:**
- `App.svelte` — root component; polls `/api/stats` every 5 seconds; shows connection status badge, config bar (gRPC port / HTTP port / data_dir / retention), summary counters (services / month buckets / total spans), top-right endpoint selector dropdown, and a grid of `ServiceCard` components
- `lib/components/ServiceCard.svelte` — per-service card showing service name, total span count, and a per-month table with inline bar charts; a **Drilldown** button fetches the last 50 spans via `/api/spans` and displays them in an inline expandable table (operation name, trace ID, start time, duration, status)
- `lib/components/TraceCard.svelte` — waterfall visualization; hovering a span name shows a **hover card** that displays all OTLP span fields decoded from `span_details`: identity (span/parent ID, trace state), timing (kind, status code + message, start, end, duration), attributes (key + formatted value for each), events (name, timestamp, per-event attributes), and links (trace/span IDs, attributes)
- `lib/types.ts` — TypeScript interfaces for all API response shapes including `OtlpSpan`, `OtlpKeyValue`, `OtlpValue`, `OtlpEvent`, `OtlpLink`, and `OtlpStatus` matching the protojson output of `opentelemetry.proto.trace.v1.Span`

**Build:**
```bash
cd cmd/dashboard/ui
npm install
npm run build   # output → dist/
```
The `dist/` directory serves as the static input for Go's `//go:embed` functionality, meaning the Go server embeds the UI assets directly into the standalone monolithic binary during build.

## File Structure

```
otelstor/
├── otelstor.cfg             # default textproto config
├── proto/
│   ├── config.proto         # ServerConfig message definition
│   ├── config.pb.go         # generated by protoc-gen-go
│   ├── stats.proto          # StatsService: GetStats, GetSpans, GetSpanTree + message types
│   ├── stats.pb.go          # generated by protoc-gen-go
│   └── stats_grpc.pb.go     # generated by protoc-gen-go-grpc
├── cmd/
│   ├── dashboard/
│   │   ├── main.go          # HTTP bridge: /api/stats → gRPC, serves static UI
│   │   └── ui/
│   │       ├── index.html
│   │       ├── package.json
│   │       ├── vite.config.js
│   │       ├── svelte.config.js
│   │       ├── dist/        # pre-built production bundle (committed)
│   │       └── src/
│   │           ├── main.js
│   │           ├── app.css
│   │           ├── App.svelte
│   │           └── lib/
│   │               ├── types.ts
│   │               └── components/
│   │                   └── ServiceCard.svelte
│   └── testclient/
│       └── main.go          # send synthetic spans (-count) or dump stored spans (-dump)
├── main.go                  # gRPC server, TraceService, StatsService, config, cleanup
└── store/
    └── store.go             # bbolt storage: write, GetSpans, GetSpanTree, BucketStats, cleanup, compression
```
