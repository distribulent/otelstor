export interface ServerConfig {
  port: number;
  http_port: number;
  data_dir: string;
  retention_days: number;
}

export interface MonthBucket {
  month: string;       // YYYY-MM
  span_count: number;
}

export interface ServiceBucket {
  name: string;
  months: MonthBucket[];
}

export interface StatsResponse {
  config: ServerConfig;
  services: ServiceBucket[];
}

// ── OTLP span detail types (from protojson-encoded opentelemetry.proto.trace.v1.Span) ──

export interface OtlpValue {
  stringValue?: string;
  intValue?: string;       // int64 encoded as string in protojson
  boolValue?: boolean;
  doubleValue?: number;
  bytesValue?: string;     // base64-encoded
  arrayValue?: { values?: OtlpValue[] };
  kvlistValue?: { values?: OtlpKeyValue[] };
}

export interface OtlpKeyValue {
  key: string;
  value?: OtlpValue;
}

export interface OtlpStatus {
  message?: string;
  code?: string;           // e.g. "STATUS_CODE_OK", "STATUS_CODE_ERROR"
}

export interface OtlpEvent {
  timeUnixNano?: string;   // uint64 as string
  name?: string;
  attributes?: OtlpKeyValue[];
  droppedAttributesCount?: number;
}

export interface OtlpLink {
  traceId?: string;
  spanId?: string;
  traceState?: string;
  attributes?: OtlpKeyValue[];
  droppedAttributesCount?: number;
  flags?: number;
}

// Full OTLP Span as returned by protojson.Marshal.
export interface OtlpSpan {
  traceId?: string;
  spanId?: string;
  traceState?: string;
  parentSpanId?: string;
  flags?: number;
  name?: string;
  kind?: string;           // e.g. "SPAN_KIND_SERVER"
  startTimeUnixNano?: string;
  endTimeUnixNano?: string;
  attributes?: OtlpKeyValue[];
  droppedAttributesCount?: number;
  events?: OtlpEvent[];
  droppedEventsCount?: number;
  links?: OtlpLink[];
  droppedLinksCount?: number;
  status?: OtlpStatus;
}

export interface SpanEntry {
  trace_id: string;
  span_id: string;
  parent_span_id: string;
  name: string;
  month: string;
  start_time_unix_nano: number;
  end_time_unix_nano: number;
  status_code: number;
  span_details?: OtlpSpan;
}

export interface SpansResponse {
  service: string;
  spans: SpanEntry[];
}

export interface SpanTreeResponse {
  trace_id: string;
  spans: SpanEntry[];
}
