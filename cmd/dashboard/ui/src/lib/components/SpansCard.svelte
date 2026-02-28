<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import type { SpanEntry } from "../types.ts";

  export let service: string;
  export let spans: SpanEntry[];

  const dispatch = createEventDispatcher<{ close: void; showTrace: string; refresh: void }>();

  // Deduplicate: keep only the earliest span (by start time) per trace_id.
  $: deduped = (() => {
    const byTrace = new Map<string, SpanEntry>();
    for (const sp of spans) {
      const existing = byTrace.get(sp.trace_id);
      if (
        !existing ||
        sp.start_time_unix_nano < existing.start_time_unix_nano
      ) {
        byTrace.set(sp.trace_id, sp);
      }
    }
    return [...byTrace.values()];
  })();

  // ── Operation name filter ─────────────────────────────────────────────────
  let filterOpen = false;
  let excludedNames: Set<string> = new Set();

  $: uniqueNames = [...new Set(deduped.map((sp) => sp.name))].sort();

  function toggleName(name: string) {
    const next = new Set(excludedNames);
    if (next.has(name)) next.delete(name);
    else next.add(name);
    excludedNames = next;
  }
  function selectAllNames() { excludedNames = new Set(); }
  function selectNoneNames() { excludedNames = new Set(uniqueNames); }

  // ── Status code filter ────────────────────────────────────────────────────
  let statusFilterOpen = false;
  let excludedStatuses: Set<number> = new Set();

  $: uniqueStatuses = [...new Set(deduped.map((sp) => sp.status_code ?? 0))].sort((a, b) => a - b);

  function toggleStatus(code: number) {
    const next = new Set(excludedStatuses);
    if (next.has(code)) next.delete(code);
    else next.add(code);
    excludedStatuses = next;
  }
  function selectAllStatuses() { excludedStatuses = new Set(); }
  function selectNoneStatuses() { excludedStatuses = new Set(uniqueStatuses); }

  // ── Combined filter ───────────────────────────────────────────────────────
  $: allVisible = excludedNames.size === 0 && excludedStatuses.size === 0;

  $: filtered = deduped.filter(
    (sp) =>
      !excludedNames.has(sp.name) &&
      !excludedStatuses.has(sp.status_code ?? 0),
  );

  function handleOutsideClick(e: MouseEvent) {
    const t = e.target as Element;
    if (filterOpen && !t.closest(".filter-wrap-name")) filterOpen = false;
    if (statusFilterOpen && !t.closest(".filter-wrap-status")) statusFilterOpen = false;
  }

  // ── Sort ──────────────────────────────────────────────────────────────────
  type SortKey = "name" | "span_id" | "start" | "duration" | "status";
  let sortKey: SortKey = "start";
  let sortAsc = true;

  function setSort(key: SortKey) {
    if (sortKey === key) {
      sortAsc = !sortAsc;
    } else {
      sortKey = key;
      sortAsc = true;
    }
  }

  $: sorted = [...filtered].sort((a, b) => {
    let cmp = 0;
    if (sortKey === "name") {
      cmp = a.name.localeCompare(b.name);
    } else if (sortKey === "span_id") {
      cmp = a.span_id.localeCompare(b.span_id);
    } else if (sortKey === "start") {
      cmp = a.start_time_unix_nano - b.start_time_unix_nano;
    } else if (sortKey === "duration") {
      const da = a.end_time_unix_nano - a.start_time_unix_nano;
      const db = b.end_time_unix_nano - b.start_time_unix_nano;
      cmp = da - db;
    } else if (sortKey === "status") {
      cmp = (a.status_code ?? 0) - (b.status_code ?? 0);
    }
    return sortAsc ? cmp : -cmp;
  });

  function sortIndicator(key: SortKey): string {
    if (sortKey !== key) return "";
    return sortAsc ? " ▲" : " ▼";
  }

  // ── Duration distribution ─────────────────────────────────────────────────
  const HIST_BUCKETS = 20;

  function fmtNs(ns: number): string {
    if (ns <= 0) return "0ns";
    if (ns < 1_000) return `${Math.round(ns)}ns`;
    if (ns < 1_000_000) return `${(ns / 1_000).toFixed(0)}µs`;
    if (ns < 1_000_000_000) return `${(ns / 1_000_000).toFixed(1)}ms`;
    return `${(ns / 1_000_000_000).toFixed(2)}s`;
  }

  $: durs = sorted.map((sp) =>
    Math.max(0, sp.end_time_unix_nano - sp.start_time_unix_nano),
  );

  $: durationStats = (() => {
    if (durs.length === 0) return null;
    const s = [...durs].sort((a, b) => a - b);
    const min = s[0];
    const max = s[s.length - 1];
    const avg = s.reduce((a, b) => a + b, 0) / s.length;
    const mid = Math.floor(s.length / 2);
    const median =
      s.length % 2 === 0 ? (s[mid - 1] + s[mid]) / 2 : s[mid];
    return { min, max, avg, median };
  })();

  $: histogram = (() => {
    if (!durationStats || durs.length === 0)
      return [] as { count: number; lo: number; hi: number }[];
    const { min, max } = durationStats;
    const range = max - min;
    if (range === 0) return [{ count: durs.length, lo: min, hi: max }];
    const counts = new Array<number>(HIST_BUCKETS).fill(0);
    for (const d of durs) {
      counts[Math.min(HIST_BUCKETS - 1, Math.floor(((d - min) / range) * HIST_BUCKETS))]++;
    }
    const bw = range / HIST_BUCKETS;
    return counts.map((count, i) => ({
      count,
      lo: min + i * bw,
      hi: min + (i + 1) * bw,
    }));
  })();

  $: maxBucketCount = Math.max(1, ...histogram.map((b) => b.count));

  function formatTime(nano: number): string {
    return new Date(nano / 1e6).toLocaleString();
  }

  function formatDur(startNano: number, endNano: number): string {
    const ns = endNano - startNano;
    if (ns < 1_000) return `${ns}ns`;
    if (ns < 1_000_000) return `${(ns / 1_000).toFixed(0)}µs`;
    if (ns < 1_000_000_000) return `${(ns / 1_000_000).toFixed(1)}ms`;
    return `${(ns / 1_000_000_000).toFixed(2)}s`;
  }

  const statusLabel: Record<number, string> = {
    0: "UNSET",
    1: "OK",
    2: "ERROR",
  };
  function fmtStatus(sp: SpanEntry): string {
    const code = sp.status_code ?? 0;
    let label = statusLabel[code] ?? String(code);
    if (code === 0 && !sp.parent_span_id && sp.span_details?.attributes) {
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
</script>

<svelte:window on:click={handleOutsideClick} />

<div class="spans-card">
  <div class="sc-header">
    <div class="sc-title">
      <span class="sc-label">Spans</span>
      <span class="sc-service">{service}</span>
      <span class="sc-count">
        {#if !allVisible}{filtered.length}/{deduped.length}{:else}{deduped.length}{/if}
        traces · {spans.length} spans
      </span>
    </div>
    <div class="sc-controls">
      <!-- Operation name filter dropdown -->
      <div class="filter-wrap-name">
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <button
          class="filter-btn"
          class:filter-btn-active={excludedNames.size > 0}
          on:click|stopPropagation={() => (filterOpen = !filterOpen)}
        >
          {excludedNames.size === 0
            ? "All operations"
            : `${uniqueNames.length - excludedNames.size}/${uniqueNames.length} operations`}
          <span class="filter-chevron">{filterOpen ? "▲" : "▼"}</span>
        </button>
        {#if filterOpen}
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <div class="filter-menu" on:click|stopPropagation>
            <div class="filter-actions">
              <button class="fa-btn" on:click={selectAllNames}>All</button>
              <button class="fa-btn" on:click={selectNoneNames}>None</button>
            </div>
            <div class="filter-list">
              {#each uniqueNames as name}
                <label class="filter-item">
                  <input
                    type="checkbox"
                    checked={!excludedNames.has(name)}
                    on:change={() => toggleName(name)}
                  />
                  <span class="filter-name">{name}</span>
                </label>
              {/each}
            </div>
          </div>
        {/if}
      </div>

      <!-- Status code filter dropdown -->
      <div class="filter-wrap-status">
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <button
          class="filter-btn"
          class:filter-btn-active={excludedStatuses.size > 0}
          on:click|stopPropagation={() => (statusFilterOpen = !statusFilterOpen)}
        >
          {excludedStatuses.size === 0
            ? "All statuses"
            : `${uniqueStatuses.length - excludedStatuses.size}/${uniqueStatuses.length} statuses`}
          <span class="filter-chevron">{statusFilterOpen ? "▲" : "▼"}</span>
        </button>
        {#if statusFilterOpen}
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <div class="filter-menu" on:click|stopPropagation>
            <div class="filter-actions">
              <button class="fa-btn" on:click={selectAllStatuses}>All</button>
              <button class="fa-btn" on:click={selectNoneStatuses}>None</button>
            </div>
            <div class="filter-list">
              {#each uniqueStatuses as code}
                <label class="filter-item">
                  <input
                    type="checkbox"
                    checked={!excludedStatuses.has(code)}
                    on:change={() => toggleStatus(code)}
                  />
                  <span class="status-dot status-dot-{code}"></span>
                  <span class="filter-name">{statusLabel[code] ?? String(code)}</span>
                </label>
              {/each}
            </div>
          </div>
        {/if}
      </div>

      <button class="refresh-btn" on:click={() => dispatch("refresh")}>↻ Refresh</button>
      <button class="close-btn" on:click={() => dispatch("close")}>✕ Close</button>
    </div>
  </div>

  {#if spans.length === 0}
    <div class="muted">No spans found.</div>
  {:else}
    {#if sorted.length > 0 && durationStats}
      <div class="dist-section">
        <div class="dist-bars">
          {#each histogram as b}
            <div
              class="dist-bar-col"
              title="{fmtNs(b.lo)}–{fmtNs(b.hi)}: {b.count} span{b.count !== 1 ? 's' : ''}"
            >
              <div
                class="dist-bar"
                style="height:{((b.count / maxBucketCount) * 100).toFixed(1)}%"
              ></div>
            </div>
          {/each}
        </div>
        <div class="dist-axis">
          <span>{fmtNs(durationStats.min)}</span>
          <span>{fmtNs(durationStats.max)}</span>
        </div>
        <div class="dist-stats">
          avg {fmtNs(Math.round(durationStats.avg))} · median {fmtNs(Math.round(durationStats.median))} · min {fmtNs(durationStats.min)} · max {fmtNs(durationStats.max)}
        </div>
      </div>
    {/if}
    <div class="span-scroll">
      <table class="span-table">
        <thead>
          <tr>
            <th class="th-num">#</th>
            <th>
              <button
                class="sort-btn"
                class:active={sortKey === "name"}
                on:click={() => setSort("name")}
              >
                Operation{sortIndicator("name")}
              </button>
            </th>
            <th>
              <button
                class="sort-btn"
                class:active={sortKey === "span_id"}
                on:click={() => setSort("span_id")}
              >
                Trace ID / Span ID{sortIndicator("span_id")}
              </button>
            </th>
            <th>
              <button
                class="sort-btn"
                class:active={sortKey === "start"}
                on:click={() => setSort("start")}
              >
                Start{sortIndicator("start")}
              </button>
            </th>
            <th class="right">
              <button
                class="sort-btn right"
                class:active={sortKey === "duration"}
                on:click={() => setSort("duration")}
              >
                Duration{sortIndicator("duration")}
              </button>
            </th>
            <th>
              <button
                class="sort-btn"
                class:active={sortKey === "status"}
                on:click={() => setSort("status")}
              >
                Status{sortIndicator("status")}
              </button>
            </th>
          </tr>
        </thead>
        <tbody>
          {#each sorted as sp, i}
            <tr>
              <td class="num">{i + 1}</td>
              <td class="op-name">{sp.name}</td>
              <td>
                <div class="id-stack">
                  <span class="mono dim">{sp.trace_id.slice(0, 16)}…</span>
                  <button
                    class="span-link"
                    on:click={() => dispatch("showTrace", sp.span_id)}
                  >
                    {sp.span_id.slice(0, 16)}…
                  </button>
                </div>
              </td>
              <td class="mono dim">{formatTime(sp.start_time_unix_nano)}</td>
              <td class="mono right"
                >{formatDur(sp.start_time_unix_nano, sp.end_time_unix_nano)}</td
              >
              <td class="status status-{sp.status_code ?? 0}"
                >{fmtStatus(sp)}</td
              >
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<style>
  .spans-card {
    background: #1a1a1a;
    border: 1px solid #2a2a2a;
    border-radius: 8px;
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  .sc-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
  }

  .sc-controls {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-shrink: 0;
  }

  .sc-title {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }

  .sc-label {
    font-size: 11px;
    font-weight: 600;
    color: #666;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    flex-shrink: 0;
  }

  .sc-service {
    font-size: 14px;
    font-weight: 600;
    color: #e0e0e0;
  }

  .sc-count {
    font-size: 12px;
    color: #666;
    flex-shrink: 0;
  }

  .refresh-btn,
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

  .refresh-btn:hover,
  .close-btn:hover {
    color: #ccc;
    border-color: #555;
  }

  .muted {
    color: #555;
    font-size: 13px;
    padding: 8px 0;
  }

  .span-scroll {
    overflow-x: auto;
  }

  .span-table {
    width: 100%;
    min-width: 600px;
    border-collapse: collapse;
  }

  .span-table th {
    padding: 0 8px 6px 0;
    border-bottom: 1px solid #2a2a2a;
    white-space: nowrap;
  }

  .span-table td {
    padding: 6px 8px 6px 0;
    border-bottom: 1px solid #1e1e1e;
    color: #c0c0c0;
    font-size: 12px;
    white-space: nowrap;
  }

  .span-table tr:last-child td {
    border-bottom: none;
  }

  .th-num {
    width: 32px;
  }

  .sort-btn {
    font-size: 11px;
    font-weight: 500;
    color: #666;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    background: none;
    border: none;
    padding: 0;
    cursor: pointer;
    white-space: nowrap;
    display: inline-flex;
    align-items: center;
    gap: 2px;
  }

  .sort-btn:hover {
    color: #aaa;
  }

  .sort-btn.active {
    color: #4dd0c4;
  }

  .right {
    text-align: right;
  }

  .num {
    color: #555;
    font-size: 11px;
    font-variant-numeric: tabular-nums;
  }

  .op-name {
    color: #d0d0d0;
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .mono {
    font-family: monospace;
  }

  .dim {
    color: #666;
  }

  .status {
    font-size: 11px;
    font-weight: 600;
    padding: 1px 6px;
    border-radius: 4px;
    font-variant-numeric: tabular-nums;
  }

  .status-0 {
    color: #888;
  }
  .status-1 {
    color: #4dd0c4;
    background: rgba(77, 208, 196, 0.1);
  }
  .status-2 {
    color: #ef5350;
    background: rgba(239, 83, 80, 0.1);
  }

  .id-stack {
    display: flex;
    flex-direction: column;
    gap: 1px;
    line-height: 1.4;
  }

  .span-link {
    font-family: monospace;
    font-size: 12px;
    color: #5ba8f5;
    background: none;
    border: none;
    cursor: pointer;
    padding: 0;
    text-decoration: underline;
    text-decoration-color: rgba(91, 168, 245, 0.4);
    text-underline-offset: 2px;
  }

  .span-link:hover {
    color: #90c8ff;
  }

  /* ── Filter dropdowns ───────────────────────────────────────────────────── */
  .filter-wrap-name,
  .filter-wrap-status {
    position: relative;
  }

  .filter-btn {
    font-size: 11px;
    font-weight: 500;
    color: #888;
    background: #222;
    border: 1px solid #333;
    border-radius: 6px;
    padding: 3px 10px;
    cursor: pointer;
    white-space: nowrap;
    display: flex;
    align-items: center;
    gap: 5px;
    transition: color 0.15s, border-color 0.15s;
  }

  .filter-btn:hover {
    color: #ccc;
    border-color: #555;
  }

  .filter-btn-active {
    color: #4dd0c4;
    border-color: rgba(77, 208, 196, 0.4);
    background: rgba(77, 208, 196, 0.08);
  }

  .filter-chevron {
    font-size: 9px;
    opacity: 0.7;
  }

  .filter-menu {
    position: absolute;
    top: calc(100% + 4px);
    right: 0;
    z-index: 50;
    background: #1c1c1c;
    border: 1px solid #333;
    border-radius: 8px;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.6);
    min-width: 200px;
    max-width: 320px;
    padding: 8px 0;
  }

  .filter-actions {
    display: flex;
    gap: 6px;
    padding: 0 10px 8px;
    border-bottom: 1px solid #2a2a2a;
    margin-bottom: 4px;
  }

  .fa-btn {
    font-size: 10px;
    font-weight: 600;
    color: #888;
    background: #2a2a2a;
    border: 1px solid #333;
    border-radius: 4px;
    padding: 2px 8px;
    cursor: pointer;
  }

  .fa-btn:hover {
    color: #ccc;
    border-color: #555;
  }

  .filter-list {
    max-height: 220px;
    overflow-y: auto;
    scrollbar-width: thin;
    scrollbar-color: #333 transparent;
  }

  .filter-item {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 10px;
    cursor: pointer;
  }

  .filter-item:hover {
    background: rgba(255, 255, 255, 0.04);
  }

  .filter-item input[type="checkbox"] {
    accent-color: #4dd0c4;
    flex-shrink: 0;
    width: 13px;
    height: 13px;
    cursor: pointer;
  }

  .filter-name {
    font-size: 12px;
    color: #c0c0c0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .status-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
  }
  .status-dot-0 { background: #555; }
  .status-dot-1 { background: #4dd0c4; }
  .status-dot-2 { background: #ef5350; }

  /* ── Duration distribution ──────────────────────────────────────────────── */
  .dist-section {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .dist-bars {
    display: flex;
    align-items: flex-end;
    gap: 2px;
    height: 56px;
    background: #141414;
    border: 1px solid #222;
    border-radius: 6px;
    padding: 6px 6px 0;
  }

  .dist-bar-col {
    flex: 1;
    display: flex;
    align-items: flex-end;
    height: 100%;
    cursor: default;
  }

  .dist-bar {
    width: 100%;
    background: linear-gradient(180deg, #4dd0c4, #2196f3);
    border-radius: 2px 2px 0 0;
    min-height: 1px;
    transition: opacity 0.1s;
  }

  .dist-bar-col:hover .dist-bar {
    opacity: 0.65;
  }

  .dist-axis {
    display: flex;
    justify-content: space-between;
    font-size: 10px;
    color: #555;
    font-family: monospace;
    padding: 0 2px;
  }

  .dist-stats {
    font-size: 11px;
    color: #666;
    font-family: monospace;
  }
</style>
