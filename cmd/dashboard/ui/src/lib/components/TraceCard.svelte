<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import type { SpanEntry, OtlpValue, OtlpKeyValue } from "../types.ts";

  export let traceId: string;
  export let spans: SpanEntry[];

  const dispatch = createEventDispatcher<{ close: void }>();

  $: sorted = [...spans].sort((a, b) =>
    a.start_time_unix_nano < b.start_time_unix_nano
      ? -1
      : a.start_time_unix_nano > b.start_time_unix_nano
        ? 1
        : 0,
  );

  $: traceStart = sorted.length > 0 ? sorted[0].start_time_unix_nano : 0;
  $: traceEnd =
    sorted.length > 0
      ? Math.max(...sorted.map((s) => s.end_time_unix_nano))
      : 1;
  $: traceDur = Math.max(1, traceEnd - traceStart);
  $: depths = buildDepths(spans);

  function buildDepths(sps: SpanEntry[]): Map<string, number> {
    const spanSet = new Set(sps.map((s) => s.span_id));
    const cache = new Map<string, number>();
    const visiting = new Set<string>();

    function depth(sp: SpanEntry): number {
      if (cache.has(sp.span_id)) return cache.get(sp.span_id)!;
      if (visiting.has(sp.span_id)) {
        cache.set(sp.span_id, 0);
        return 0;
      }
      if (!sp.parent_span_id || !spanSet.has(sp.parent_span_id)) {
        cache.set(sp.span_id, 0);
        return 0;
      }
      visiting.add(sp.span_id);
      const parent = sps.find((s) => s.span_id === sp.parent_span_id);
      const d = parent ? depth(parent) + 1 : 0;
      visiting.delete(sp.span_id);
      cache.set(sp.span_id, d);
      return d;
    }

    sps.forEach((sp) => depth(sp));
    return cache;
  }

  function barLeft(sp: SpanEntry): string {
    return (
      (((sp.start_time_unix_nano - traceStart) / traceDur) * 100).toFixed(3) +
      "%"
    );
  }

  function barWidth(sp: SpanEntry): string {
    const durNs = Math.max(0, sp.end_time_unix_nano - sp.start_time_unix_nano);
    return Math.max(0.4, (durNs / traceDur) * 100).toFixed(3) + "%";
  }

  function formatDur(ns: number): string {
    if (ns <= 0) return "0ns";
    if (ns < 1_000) return `${ns}ns`;
    if (ns < 1_000_000) return `${(ns / 1_000).toFixed(0)}µs`;
    if (ns < 1_000_000_000) return `${(ns / 1_000_000).toFixed(1)}ms`;
    return `${(ns / 1_000_000_000).toFixed(2)}s`;
  }

  function formatTime(nano: number): string {
    return new Date(nano / 1e6).toLocaleString();
  }

  $: totalDurLabel = formatDur(traceDur);

  const statusLabel: Record<number, string> = {
    0: "UNSET",
    1: "OK",
    2: "ERROR",
  };

  function getStatusLabel(sp: SpanEntry, depth: number): string {
    const code = sp.status_code ?? 0;
    let label = statusLabel[code] ?? String(code);
    if (code === 0 && depth === 0 && sp.span_details?.attributes) {
      const attr = sp.span_details.attributes.find(
        (a) => a.key === "rpc.response.status_code",
      );
      if (attr?.value) {
        const val = attr.value.intValue ?? attr.value.stringValue;
        if (val !== undefined) {
          label += ` (${val})`;
        }
      }
    }
    return label;
  }

  // ── OTLP helpers ─────────────────────────────────────────────────────────────

  function formatOtlpValue(v: OtlpValue | undefined): string {
    if (!v) return "";
    if (v.stringValue !== undefined) return v.stringValue;
    if (v.intValue !== undefined) return v.intValue;
    if (v.boolValue !== undefined) return String(v.boolValue);
    if (v.doubleValue !== undefined) return String(v.doubleValue);
    if (v.bytesValue !== undefined) return `<bytes: ${v.bytesValue}>`;
    if (v.arrayValue)
      return `[${(v.arrayValue.values ?? []).map(formatOtlpValue).join(", ")}]`;
    if (v.kvlistValue)
      return `{${(v.kvlistValue.values ?? []).map((kv) => `${kv.key}=${formatOtlpValue(kv.value)}`).join(", ")}}`;
    return "";
  }

  function formatKind(kind: string | undefined): string {
    if (!kind) return "";
    return kind.replace("SPAN_KIND_", "");
  }

  function fmtNanoTs(nanoStr: string | undefined): string {
    if (!nanoStr) return "";
    return formatTime(Number(nanoStr));
  }

  // ── Hover card ───────────────────────────────────────────────────────────────
  let hoveredSpan: SpanEntry | null = null;
  let hoverX = 0;
  let hoverY = 0;

  function onNameEnter(e: MouseEvent, sp: SpanEntry) {
    hoveredSpan = sp;
    positionHover(e);
  }

  function onNameMove(e: MouseEvent) {
    positionHover(e);
  }

  function onNameLeave() {
    hoveredSpan = null;
  }

  function positionHover(e: MouseEvent) {
    const margin = 12;
    const cardW = 420;
    const maxH = window.innerHeight * 0.72;
    hoverX =
      e.clientX + margin + cardW > window.innerWidth
        ? e.clientX - cardW - margin
        : e.clientX + margin;
    hoverY =
      e.clientY + margin + maxH > window.innerHeight
        ? Math.max(8, window.innerHeight - maxH - margin)
        : e.clientY + margin;
  }
</script>

<div class="trace-card">
  <div class="tc-header">
    <div class="tc-title">
      <span class="tc-label">Trace</span>
      <span class="tc-id">{traceId}</span>
      <span class="tc-dur">{totalDurLabel} · {sorted.length} spans</span>
    </div>
    <button class="close-btn" on:click={() => dispatch("close")}>✕ Close</button
    >
  </div>

  <div class="waterfall">
    <!-- Ruler -->
    <div class="ruler-row">
      <div class="name-col"></div>
      <div class="time-col">
        <div class="ruler">
          <span class="tick" style="left:0%">0</span>
          <span class="tick" style="left:25%">{formatDur(traceDur * 0.25)}</span
          >
          <span class="tick" style="left:50%">{formatDur(traceDur * 0.5)}</span>
          <span class="tick" style="left:75%">{formatDur(traceDur * 0.75)}</span
          >
          <span class="tick tick-right" style="left:100%">{totalDurLabel}</span>
        </div>
      </div>
    </div>

    <!-- Span rows -->
    {#each sorted as sp}
      {@const d = depths.get(sp.span_id) ?? 0}
      {@const spanNs = Math.max(
        0,
        sp.end_time_unix_nano - sp.start_time_unix_nano,
      )}
      {@const code = sp.status_code ?? 0}
      <div class="span-row">
        <div class="name-col" style="padding-left: {d * 14 + 8}px">
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <span
            class="span-name"
            on:mouseenter={(e) => onNameEnter(e, sp)}
            on:mousemove={onNameMove}
            on:mouseleave={onNameLeave}>{sp.name}</span
          >
          <span class="span-status status-{code}">{getStatusLabel(sp, d)}</span>
        </div>
        <div class="time-col">
          <div
            class="span-bar bar-{code}"
            style="left:{barLeft(sp)};width:{barWidth(sp)}"
            title="{sp.name} ({formatDur(spanNs)})"
          ></div>
          <span
            class="bar-label"
            style="left:calc({barLeft(sp)} + {barWidth(sp)} + 4px)"
            >{formatDur(spanNs)}</span
          >
        </div>
      </div>
    {/each}
  </div>
</div>

<!-- Hover card (fixed, outside the scroll container) -->
{#if hoveredSpan}
  {@const sp = hoveredSpan}
  {@const code = sp.status_code ?? 0}
  {@const spanNs = Math.max(0, sp.end_time_unix_nano - sp.start_time_unix_nano)}
  {@const d = sp.span_details}
  <div class="hover-card" style="left:{hoverX}px;top:{hoverY}px">
    <div class="hc-name">{sp.name}</div>

    <!-- Identity -->
    <div class="hc-grid">
      <span class="hc-key">Span ID</span>
      <span class="hc-val mono">{sp.span_id}</span>
      {#if sp.parent_span_id}
        <span class="hc-key">Parent</span>
        <span class="hc-val mono">{sp.parent_span_id}</span>
      {/if}
      {#if d?.traceState}
        <span class="hc-key">Trace State</span>
        <span class="hc-val mono">{d.traceState}</span>
      {/if}
    </div>

    <!-- Timing & Status -->
    <div class="hc-section">Timing</div>
    <div class="hc-grid">
      {#if d?.kind && d.kind !== "SPAN_KIND_UNSPECIFIED"}
        <span class="hc-key">Kind</span>
        <span class="hc-val">{formatKind(d.kind)}</span>
      {/if}
      <span class="hc-key">Status</span>
      <span class="hc-val status-text status-text-{code}"
        >{getStatusLabel(sp, depths.get(sp.span_id) ?? 0)}</span
      >
      {#if d?.status?.message}
        <span class="hc-key">Message</span>
        <span class="hc-val">{d.status.message}</span>
      {/if}
      <span class="hc-key">Start</span>
      <span class="hc-val mono">{formatTime(sp.start_time_unix_nano)}</span>
      <span class="hc-key">End</span>
      <span class="hc-val mono">{formatTime(sp.end_time_unix_nano)}</span>
      <span class="hc-key">Duration</span>
      <span class="hc-val mono">{formatDur(spanNs)}</span>
    </div>

    <!-- Attributes -->
    {#if d?.attributes && d.attributes.length > 0}
      <div class="hc-section">
        Attributes ({d.attributes.length}{(d.droppedAttributesCount ?? 0) > 0
          ? ` +${d.droppedAttributesCount} dropped`
          : ""})
      </div>
      <div class="hc-grid hc-attrs">
        {#each d.attributes as attr}
          <span class="hc-key hc-attr-key">{attr.key}</span>
          <span class="hc-val mono">{formatOtlpValue(attr.value)}</span>
        {/each}
      </div>
    {:else if (d?.droppedAttributesCount ?? 0) > 0}
      <div class="hc-section">Attributes</div>
      <div class="hc-dropped">{d!.droppedAttributesCount} dropped</div>
    {/if}

    <!-- Events -->
    {#if d?.events && d.events.length > 0}
      <div class="hc-section">
        Events ({d.events.length}{(d.droppedEventsCount ?? 0) > 0
          ? ` +${d.droppedEventsCount} dropped`
          : ""})
      </div>
      {#each d.events as ev}
        <div class="hc-event">
          <div class="hc-event-header">
            <span class="hc-event-name">{ev.name ?? "(unnamed)"}</span>
            {#if ev.timeUnixNano}
              <span class="hc-event-time">{fmtNanoTs(ev.timeUnixNano)}</span>
            {/if}
          </div>
          {#if ev.attributes && ev.attributes.length > 0}
            <div class="hc-grid hc-attrs">
              {#each ev.attributes as attr}
                <span class="hc-key hc-attr-key">{attr.key}</span>
                <span class="hc-val mono">{formatOtlpValue(attr.value)}</span>
              {/each}
            </div>
          {/if}
        </div>
      {/each}
    {/if}

    <!-- Links -->
    {#if d?.links && d.links.length > 0}
      <div class="hc-section">Links ({d.links.length})</div>
      {#each d.links as link, i}
        <div class="hc-event">
          <div class="hc-grid">
            <span class="hc-key">Trace</span>
            <span class="hc-val mono">{link.traceId ?? "—"}</span>
            <span class="hc-key">Span</span>
            <span class="hc-val mono">{link.spanId ?? "—"}</span>
            {#if link.traceState}
              <span class="hc-key">State</span>
              <span class="hc-val mono">{link.traceState}</span>
            {/if}
          </div>
          {#if link.attributes && link.attributes.length > 0}
            <div class="hc-grid hc-attrs">
              {#each link.attributes as attr}
                <span class="hc-key hc-attr-key">{attr.key}</span>
                <span class="hc-val mono">{formatOtlpValue(attr.value)}</span>
              {/each}
            </div>
          {/if}
        </div>
      {/each}
    {/if}
  </div>
{/if}

<style>
  .trace-card {
    background: #1a1a1a;
    border: 1px solid #2a2a2a;
    border-radius: 8px;
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  .tc-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
  }

  .tc-title {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
    min-width: 0;
  }

  .tc-label {
    font-size: 11px;
    font-weight: 600;
    color: #666;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    flex-shrink: 0;
  }

  .tc-id {
    font-family: monospace;
    font-size: 12px;
    color: #4dd0c4;
    word-break: break-all;
  }

  .tc-dur {
    font-size: 12px;
    color: #666;
    flex-shrink: 0;
  }

  .close-btn {
    font-size: 12px;
    color: #666;
    background: #222;
    border: 1px solid #333;
    border-radius: 6px;
    padding: 4px 12px;
    cursor: pointer;
    white-space: nowrap;
    flex-shrink: 0;
  }

  .close-btn:hover {
    color: #ccc;
    border-color: #555;
  }

  .waterfall {
    display: flex;
    flex-direction: column;
    gap: 1px;
    overflow-x: auto;
  }

  .ruler-row,
  .span-row {
    display: flex;
    align-items: center;
    min-width: 700px;
  }

  .name-col {
    width: 220px;
    flex-shrink: 0;
    display: flex;
    align-items: center;
    gap: 6px;
    overflow: hidden;
    padding: 4px 8px 4px 8px;
    box-sizing: border-box;
  }

  .time-col {
    flex: 1;
    position: relative;
    height: 28px;
    border-left: 1px solid #2a2a2a;
    margin-left: 4px;
  }

  /* Ruler */
  .ruler {
    position: relative;
    height: 100%;
    border-bottom: 1px solid #2a2a2a;
  }

  .ruler-row .time-col {
    height: 20px;
  }

  .tick {
    position: absolute;
    font-size: 10px;
    color: #555;
    transform: translateX(-50%);
    bottom: 3px;
    white-space: nowrap;
  }

  .tick-right {
    transform: translateX(-100%);
  }

  /* Span rows */
  .span-row:hover {
    background: rgba(255, 255, 255, 0.02);
    border-radius: 4px;
  }

  .span-name {
    font-size: 12px;
    color: #c0c0c0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
    flex: 1;
    cursor: default;
  }

  .span-name:hover {
    color: #e0e0e0;
  }

  .span-status {
    font-size: 10px;
    font-weight: 600;
    flex-shrink: 0;
  }

  .span-status.status-0 {
    color: #555;
  }
  .span-status.status-1 {
    color: #4dd0c4;
  }
  .span-status.status-2 {
    color: #ef5350;
  }

  /* Gantt bars */
  .span-bar {
    position: absolute;
    top: 50%;
    transform: translateY(-50%);
    height: 14px;
    border-radius: 3px;
    min-width: 2px;
  }

  .bar-0 {
    background: #4a5568;
  }
  .bar-1 {
    background: linear-gradient(90deg, #4dd0c4, #2196f3);
  }
  .bar-2 {
    background: #c62828;
  }

  .span-row:hover .span-bar {
    filter: brightness(1.25);
  }

  .bar-label {
    position: absolute;
    top: 50%;
    transform: translateY(-50%);
    font-size: 10px;
    color: #666;
    white-space: nowrap;
    font-family: monospace;
    pointer-events: none;
  }

  /* Hover card */
  .hover-card {
    position: fixed;
    z-index: 100;
    background: #141414;
    border: 1px solid #333;
    border-radius: 8px;
    padding: 12px 14px;
    width: 420px;
    max-height: 72vh;
    overflow-y: auto;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.6);
    pointer-events: none;
    scrollbar-width: thin;
    scrollbar-color: #333 transparent;
  }

  .hc-name {
    font-size: 13px;
    font-weight: 600;
    color: #e0e0e0;
    margin-bottom: 10px;
    word-break: break-word;
  }

  /* Section title */
  .hc-section {
    font-size: 9px;
    font-weight: 700;
    color: #555;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    margin-top: 10px;
    margin-bottom: 4px;
    border-top: 1px solid #222;
    padding-top: 8px;
  }

  .hc-grid {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 3px 10px;
    align-items: baseline;
  }

  .hc-attrs {
    gap: 2px 8px;
  }

  .hc-key {
    font-size: 10px;
    font-weight: 500;
    color: #666;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    white-space: nowrap;
  }

  /* Attribute keys use monospace and allow wrapping — they can be long */
  .hc-attr-key {
    font-family: monospace;
    font-size: 10px;
    text-transform: none;
    letter-spacing: 0;
    color: #888;
    word-break: break-all;
    white-space: normal;
  }

  .hc-val {
    font-size: 11px;
    color: #c0c0c0;
    word-break: break-all;
  }

  .hc-val.mono {
    font-family: monospace;
  }

  .hc-dropped {
    font-size: 10px;
    color: #555;
    font-style: italic;
    margin-top: 2px;
  }

  /* Event / Link blocks */
  .hc-event {
    background: #1c1c1c;
    border: 1px solid #2a2a2a;
    border-radius: 4px;
    padding: 6px 8px;
    margin-top: 4px;
  }

  .hc-event-header {
    display: flex;
    align-items: baseline;
    gap: 8px;
    margin-bottom: 4px;
  }

  .hc-event-name {
    font-size: 11px;
    font-weight: 600;
    color: #c0c0c0;
    word-break: break-word;
  }

  .hc-event-time {
    font-size: 10px;
    color: #555;
    font-family: monospace;
    white-space: nowrap;
    flex-shrink: 0;
  }

  .status-text-0 {
    color: #888;
  }
  .status-text-1 {
    color: #4dd0c4;
    font-weight: 600;
  }
  .status-text-2 {
    color: #ef5350;
    font-weight: 600;
  }
</style>
