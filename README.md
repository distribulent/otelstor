![oteldash](https://github.com/distribulent/otelstor/blob/main/media/oteldash.png?raw=true)

# otelstor

A lightweight OpenTelemetry trace collector and storage backend. Receives spans over OTLP (gRPC and HTTP/protobuf), stores them in a local [bbolt](https://github.com/etcd-io/bbolt) database, and exposes a gRPC stats API and a Svelte web dashboard.

## Building

```bash
# Build the server
go build -o otelstor .

# Build the dashboard bridge
go build -o dashboard ./cmd/dashboard

# Build the test client
go build -o testclient ./cmd/testclient

# Build the Svelte UI (requires Node.js)
cd cmd/dashboard/ui && npm install && npm run build
```

## Running the server

```bash
./otelstor
# or with a custom config file
./otelstor -config /etc/otelstor/prod.cfg
```

### Server flags

| Flag | Default | Description |
|---|---|---|
| `-config` | `otelstor.cfg` | Path to textproto config file |

### Configuration file (`otelstor.cfg`)

The config file uses textproto format. All fields are optional; missing values use the defaults shown.

```
port: 4317
http_port: 4318
data_dir: "."
retention_days: 60
```

| Field | Default | Description |
|---|---|---|
| `port` | `4317` | OTLP gRPC listen port |
| `http_port` | `4318` | OTLP HTTP/protobuf listen port (`0` to disable) |
| `data_dir` | `"."` | Directory where `traces.db` is stored |
| `retention_days` | `60` | Days to retain trace data; older month buckets are deleted hourly |

If `otelstor.cfg` is absent the server starts with the defaults above and logs a warning. An explicit `-config` path that does not exist is a fatal error.

## OTLP endpoints

### gRPC (port 4317)

Standard OTLP gRPC — compatible with any OpenTelemetry SDK or collector configured with an `otlp/grpc` exporter:

```yaml
# OpenTelemetry Collector config example
exporters:
  otlp:
    endpoint: localhost:4317
    tls:
      insecure: true
```

### HTTP/protobuf (port 4318)

Standard OTLP HTTP/protobuf — compatible with any SDK or collector configured with an `otlp/http` exporter:

```bash
# Manual example
curl -X POST http://localhost:4318/v1/traces \
  -H "Content-Type: application/x-protobuf" \
  --data-binary @export_request.pb
```

## Running the dashboard

The dashboard is a separate Go binary that bridges the browser to the otelstor gRPC server.

```bash
./dashboard
# or
./dashboard -grpc-addrs localhost:4317,localhost:4327 -port 10731
```

Then open [http://localhost:10731](http://localhost:10731).

### Dashboard flags

| Flag | Default | Description |
|---|---|---|
| `-grpc-addrs` | `localhost:4317` | Comma-separated list of otelstor gRPC server addresses to connect to |
| `-port` | `10731` | HTTP port for the dashboard |

Each service card has a **Drilldown** button. Clicking it fetches the last 50 spans for that service and displays them inline — showing operation name, trace ID, start time, duration, and status. Click again to collapse.

### Dashboard UI development

```bash
cd cmd/dashboard/ui
npm install
npm run dev     # Vite dev server on :5173, proxies /api to :10731
npm run build   # production build → dist/
```

## Test client

The test client sends synthetic OTLP spans to the server, or dumps stored spans for a service. Spans have random start times up to 1 hour in the past and random durations up to 2 seconds.

### Send spans

```bash
./testclient
./testclient -service payment-svc -count 500
./testclient -addr localhost:4317 -service worker -count 50
```

### Dump stored spans

```bash
# Dump last 50 spans for the default service ("frontend")
./testclient -dump

# Dump last 50 spans for a specific service
./testclient -service payment-svc -dump
```

### List trace IDs

```bash
# List the last 100 unique trace IDs for the default service
./testclient -traces

# List trace IDs for a specific service
./testclient -service payment-svc -traces
```

### Show a trace by trace ID

```bash
./testclient -trace <hex-trace-id>
```

Fetches all spans for the given trace ID and prints them as an indented tree (root spans at the top, children indented under their parent, each group sorted by start time). The `OFFSET` column shows time elapsed since the earliest span in the trace.

Output example:
```
Trace 4bf92f3577b34da6a3ce929d0e0e4736 — 4 span(s)

  SPAN                                              SPAN ID              OFFSET  DURATION  STATUS
  ------------------------------------------------  ------------------  ------  --------  ------
  GET /api/orders                                   a2fb4a1d1a96d31…       0µs    120ms  OK
    POST /api/payments                              c9a0c77e2d19b7b…      10ms     80ms  OK
      db.query                                      b14f87cce43d191…      12ms     60ms  OK
    cache.get                                       f3e9a245301e9b2…     100ms      5ms  OK
```

### List all services

```bash
# Show all services with trace count, span count, and last-updated time
./testclient -services
```

Output example:
```
3 service(s)

  SERVICE                          TRACES     SPANS  LAST UPDATED
  -------                          ------     -----  ------------
  frontend                            42       1024  2026-02-22 10:30:05 UTC
  payment-svc                         17        380  2026-02-21 18:14:22 UTC
  worker                               5         95  2026-02-20 09:00:01 UTC
```

### Test client flags

| Flag | Default | Description |
|---|---|---|
| `-addr` | `localhost:4317` | otelstor gRPC server address |
| `-service` | `frontend` | Service name |
| `-count` | `100` | Number of spans to send (send mode only) |
| `-dump` | `false` | Dump last 50 stored entries instead of sending |
| `-traces` | `false` | List last 100 unique trace IDs instead of sending |
| `-services` | `false` | List all services with trace count, span count, and last-updated time |
| `-trace` | `""` | Hex trace ID to fetch and display as an indented span tree |

## gRPC Stats API

The `otelstor.StatsService` is available on the same gRPC port as the OTLP endpoint.

### `GetStats`

Returns effective server configuration and the full bucket tree (services → months → span counts).

### `GetSpans`

Returns the most recent spans for a named service.

| Field | Description |
|---|---|
| `service` | Service name to query |
| `limit` | Max spans to return (default 50) |

### `GetSpanTree`

Given any `span_id` (hex), returns all spans that share the same `trace_id` within a ±2-minute window. Spans are returned with `parent_span_id` set so the caller can reconstruct the full trace tree.

| Field | Description |
|---|---|
| `span_id` | Hex-encoded span ID of any span in the trace |

### `GetTraceByID`

Given a hex-encoded `trace_id`, returns all spans belonging to that trace by scanning every service and month bucket. Results are returned as a `GetSpanTreeResponse` (same shape as `GetSpanTree`). Returns `NOT_FOUND` if no spans match.

| Field | Description |
|---|---|
| `trace_id` | Hex-encoded trace ID |

### `GetTraceIDs`

Returns the last N unique trace IDs for a named service, ordered most-recent first.

| Field | Description |
|---|---|
| `service` | Service name to query |
| `limit` | Max trace IDs to return (default 100) |

### `ListServices`

Returns a summary for every stored service in one call.

Each `ServiceSummary` contains:

| Field | Description |
|---|---|
| `name` | Service name |
| `trace_count` | Distinct trace IDs across all stored spans |
| `span_count` | Total stored spans |
| `last_updated_unix_nano` | Start time of the most recently stored span (from ULID key) |

## Storage layout

```
traces.db  (bbolt)
└── <service-name>/          ← top-level bucket per service
    └── <YYYY-MM>/           ← sub-bucket per month
        └── <24-byte key>    ← ULID(startTime)[16] || spanId[8]
            → zlib-compressed ResourceSpans protobuf (single span)
```

Month buckets older than `retention_days` are automatically removed on an hourly schedule.
