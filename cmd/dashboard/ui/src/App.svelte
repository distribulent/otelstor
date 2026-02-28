<script lang="ts">
  import { onMount, tick } from "svelte";
  import type {
    StatsResponse,
    SpanTreeResponse,
    SpanEntry,
  } from "./lib/types.ts";
  import ServiceCard from "./lib/components/ServiceCard.svelte";
  import SpansCard from "./lib/components/SpansCard.svelte";
  import TraceCard from "./lib/components/TraceCard.svelte";

  let stats: StatsResponse | null = null;
  let loading = true;
  let error: string | null = null;
  let lastUpdate: Date | null = null;
  let status: "connecting" | "live" | "error" = "connecting";

  // ── Backends ─────────────────────────────────────────────────────────────────
  let backends: string[] = [];
  let selectedBackend = "";

  async function fetchBackends() {
    try {
      const res = await fetch("/api/backends");
      if (res.ok) {
        backends = await res.json();
        if (backends.length > 0 && !selectedBackend) {
          selectedBackend = backends[0];
        }
      }
    } catch (e) {
      console.error("Failed to fetch backends:", e);
    }
  }

  function getQueryUrl(path: string): string {
    if (!selectedBackend) return path;
    const url = new URL(`http://localhost${path}`);
    url.searchParams.set("backend", selectedBackend);
    return url.pathname + url.search;
  }

  // ── Spans drilldown ──────────────────────────────────────────────────────────
  let activeSpansService: string | null = null;
  let activeSpansData: SpanEntry[] | null = null;
  let spansLoading = false;
  let spansError: string | null = null;

  async function handleShowSpans(e: CustomEvent<string>) {
    const svc = e.detail;
    if (svc === activeSpansService) {
      // Toggle off when clicking the same service's button again.
      activeSpansService = null;
      activeSpansData = null;
      return;
    }
    activeSpansService = svc;
    activeSpansData = null;
    spansLoading = true;
    spansError = null;
    try {
      const res = await fetch(
        getQueryUrl(`/api/spans?service=${encodeURIComponent(svc)}&limit=500`),
      );
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      activeSpansData = data.spans ?? [];
      await tick();
      document
        .querySelector(".spans-section")
        ?.scrollIntoView({ behavior: "smooth" });
    } catch (e: any) {
      spansError = e.message ?? "Unknown error";
      activeSpansService = null;
    } finally {
      spansLoading = false;
    }
  }

  async function refreshSpans() {
    if (!activeSpansService) return;
    spansLoading = true;
    spansError = null;
    try {
      const res = await fetch(
        getQueryUrl(`/api/spans?service=${encodeURIComponent(activeSpansService)}&limit=500`),
      );
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      activeSpansData = data.spans ?? [];
    } catch (e: any) {
      spansError = e.message ?? "Unknown error";
    } finally {
      spansLoading = false;
    }
  }

  // ── Span tree ────────────────────────────────────────────────────────────────
  let activeTrace: SpanTreeResponse | null = null;
  let traceLoading = false;
  let traceError: string | null = null;

  async function handleShowTrace(e: CustomEvent<string>) {
    const spanId = e.detail;
    traceLoading = true;
    traceError = null;
    activeTrace = null;
    try {
      const res = await fetch(
        getQueryUrl(`/api/spantree?span_id=${encodeURIComponent(spanId)}`),
      );
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      activeTrace = await res.json();
      await tick();
      document
        .querySelector(".trace-section")
        ?.scrollIntoView({ behavior: "smooth" });
    } catch (e: any) {
      traceError = e.message ?? "Unknown error";
    } finally {
      traceLoading = false;
    }
  }

  // ── Delete service ───────────────────────────────────────────────────────────
  async function handleDeleteService(e: CustomEvent<string>) {
    const svc = e.detail;
    const res = await fetch(
      getQueryUrl(`/api/service?service=${encodeURIComponent(svc)}`),
      { method: "DELETE" },
    );
    if (!res.ok) {
      alert(`Failed to delete "${svc}": HTTP ${res.status}`);
      return;
    }
    // Clear any open panels for this service.
    if (activeSpansService === svc) {
      activeSpansService = null;
      activeSpansData = null;
    }
    if (activeTrace?.spans?.some((s) => s.name)) {
      /* keep trace open, it may be cross-service */
    }
    await fetchStats();
  }

  // ── Stats polling ────────────────────────────────────────────────────────────
  async function fetchStats() {
    try {
      const res = await fetch(getQueryUrl("/api/stats"));
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      stats = await res.json();
      lastUpdate = new Date();
      status = "live";
      error = null;
    } catch (e: any) {
      error = e.message ?? "Unknown error";
      status = "error";
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    fetchBackends().then(() => fetchStats());
    const id = setInterval(fetchStats, 5000);
    return () => clearInterval(id);
  });

  function onBackendChange() {
    // Clear active UI state when switching backends
    activeSpansService = null;
    activeSpansData = null;
    activeTrace = null;
    fetchStats();
  }

  $: totalSpans = (stats?.services ?? []).reduce(
    (sum, svc) =>
      sum + (svc.months ?? []).reduce((s, m) => s + (m.span_count ?? 0), 0),
    0,
  );

  $: totalMonths = (stats?.services ?? []).reduce(
    (sum, svc) => sum + (svc.months ?? []).length,
    0,
  );

  function formatCount(n: number): string {
    if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + "M";
    if (n >= 1_000) return (n / 1_000).toFixed(1) + "K";
    return String(n);
  }

  function fmtTime(d: Date): string {
    return d.toLocaleTimeString();
  }
</script>

<div class="layout">
  <header>
    <div class="title-row">
      <h1>otelstor</h1>
      <span class="badge badge-{status}">
        {status === "connecting"
          ? "Connecting"
          : status === "live"
            ? "Live"
            : "Error"}
      </span>
      {#if lastUpdate}
        <span class="last-update">Updated {fmtTime(lastUpdate)}</span>
      {/if}
    </div>

    <div class="header-right">
      {#if backends.length > 0}
        <div class="backend-selector">
          <label for="backend-select">Backend:</label>
          <select
            id="backend-select"
            bind:value={selectedBackend}
            on:change={onBackendChange}
          >
            {#each backends as backend}
              <option value={backend}>{backend}</option>
            {/each}
          </select>
        </div>
      {/if}
    </div>
  </header>

  {#if loading}
    <div class="centered"><span class="muted">Loading…</span></div>
  {:else if error}
    <div class="error-banner">
      <span>Failed to fetch stats: {error}</span>
      <button on:click={fetchStats}>Retry</button>
    </div>
  {:else if stats}
    <section class="config-bar">
      <div class="config-item">
        <span class="label">gRPC port</span>
        <span class="value">{stats.config?.port ?? "—"}</span>
      </div>
      <div class="config-item">
        <span class="label">HTTP port</span>
        <span class="value">{stats.config?.http_port || "disabled"}</span>
      </div>
      <div class="config-item">
        <span class="label">Data dir</span>
        <span class="value mono">{stats.config?.data_dir ?? "—"}</span>
      </div>
      <div class="config-item">
        <span class="label">Retention</span>
        <span class="value">{stats.config?.retention_days ?? "—"}d</span>
      </div>
    </section>

    <section class="summary-bar">
      <div class="summary-item">
        <span class="summary-value">{stats.services?.length ?? 0}</span>
        <span class="summary-label">Services</span>
      </div>
      <div class="summary-item">
        <span class="summary-value">{totalMonths}</span>
        <span class="summary-label">Month buckets</span>
      </div>
      <div class="summary-item">
        <span class="summary-value">{formatCount(totalSpans)}</span>
        <span class="summary-label">Total spans</span>
      </div>
    </section>

    <section class="services-section">
      <h2>Services</h2>

      {#if !stats.services || stats.services.length === 0}
        <div class="centered muted">No trace data stored yet.</div>
      {:else}
        <div class="services-grid">
          {#each stats.services as svc (svc.name)}
            <ServiceCard
              service={svc}
              activeSpanService={activeSpansService}
              on:showSpans={handleShowSpans}
              on:showTrace={handleShowTrace}
              on:deleteService={handleDeleteService}
            />
          {/each}
        </div>
      {/if}
    </section>

    {#if traceLoading}
      <section class="trace-section">
        <div class="centered"><span class="muted">Loading trace…</span></div>
      </section>
    {:else if traceError}
      <section class="trace-section">
        <div class="error-banner">
          <span>Failed to load trace: {traceError}</span>
          <button on:click={() => (traceError = null)}>Dismiss</button>
        </div>
      </section>
    {:else if activeTrace}
      <section class="trace-section">
        <TraceCard
          traceId={activeTrace.trace_id}
          spans={activeTrace.spans ?? []}
          on:close={() => (activeTrace = null)}
        />
      </section>
    {/if}

    {#if spansLoading}
      <section class="spans-section">
        <div class="centered"><span class="muted">Loading spans…</span></div>
      </section>
    {:else if spansError}
      <section class="spans-section">
        <div class="error-banner">
          <span>Failed to load spans: {spansError}</span>
          <button
            on:click={() => {
              spansError = null;
              activeSpansService = null;
            }}>Dismiss</button
          >
        </div>
      </section>
    {:else if activeSpansData && activeSpansService}
      <section class="spans-section">
        <SpansCard
          service={activeSpansService}
          spans={activeSpansData}
          on:close={() => {
            activeSpansService = null;
            activeSpansData = null;
          }}
          on:refresh={refreshSpans}
          on:showTrace={handleShowTrace}
        />
      </section>
    {/if}
  {/if}
</div>

<style>
  .layout {
    max-width: 1100px;
    margin: 0 auto;
    padding: 24px 20px 48px;
    display: flex;
    flex-direction: column;
    gap: 24px;
  }

  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
  }

  .title-row {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .header-right {
    display: flex;
    align-items: center;
    gap: 16px;
  }

  .backend-selector {
    display: flex;
    align-items: center;
    gap: 8px;
    background: #1a1a1a;
    border: 1px solid #2a2a2a;
    padding: 6px 12px;
    border-radius: 6px;
  }

  .backend-selector label {
    font-size: 11px;
    color: #666;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .backend-selector select {
    background: transparent;
    border: none;
    color: #e0e0e0;
    font-size: 13px;
    font-weight: 500;
    outline: none;
    cursor: pointer;
    font-family: inherit;
  }

  .backend-selector select option {
    background: #1a1a1a;
    color: #e0e0e0;
  }

  h1 {
    font-size: 22px;
    font-weight: 700;
    color: #f0f0f0;
    letter-spacing: -0.02em;
  }

  h2 {
    font-size: 13px;
    font-weight: 600;
    color: #666;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    margin-bottom: 12px;
  }

  .badge {
    font-size: 11px;
    font-weight: 600;
    padding: 2px 10px;
    border-radius: 10px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  .badge-connecting {
    background: #2a2a2a;
    color: #888;
  }
  .badge-live {
    background: rgba(77, 208, 196, 0.15);
    color: #4dd0c4;
    border: 1px solid rgba(77, 208, 196, 0.3);
  }
  .badge-error {
    background: rgba(239, 83, 80, 0.15);
    color: #ef5350;
    border: 1px solid rgba(239, 83, 80, 0.3);
  }

  .last-update {
    font-size: 12px;
    color: #555;
  }

  .config-bar {
    display: flex;
    gap: 0;
    background: #1a1a1a;
    border: 1px solid #2a2a2a;
    border-radius: 8px;
    overflow: hidden;
  }

  .config-item {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 14px 20px;
    border-right: 1px solid #2a2a2a;
  }

  .config-item:last-child {
    border-right: none;
  }

  .label {
    font-size: 11px;
    color: #666;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .value {
    font-size: 15px;
    font-weight: 500;
    color: #e0e0e0;
  }

  .mono {
    font-family: monospace;
    font-size: 13px;
  }

  .summary-bar {
    display: flex;
    gap: 16px;
  }

  .summary-item {
    flex: 1;
    background: #1a1a1a;
    border: 1px solid #2a2a2a;
    border-radius: 8px;
    padding: 16px 20px;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .summary-value {
    font-size: 28px;
    font-weight: 700;
    color: #e0e0e0;
    line-height: 1;
    font-variant-numeric: tabular-nums;
  }

  .summary-label {
    font-size: 12px;
    color: #666;
  }

  .services-section {
    display: flex;
    flex-direction: column;
  }

  .services-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
    gap: 16px;
  }

  .error-banner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    background: rgba(239, 83, 80, 0.1);
    border: 1px solid rgba(239, 83, 80, 0.3);
    border-radius: 8px;
    padding: 14px 20px;
    color: #ef9a9a;
    font-size: 13px;
  }

  .error-banner button {
    background: rgba(239, 83, 80, 0.2);
    border: 1px solid rgba(239, 83, 80, 0.4);
    color: #ef9a9a;
    border-radius: 6px;
    padding: 5px 14px;
    cursor: pointer;
    font-size: 12px;
    white-space: nowrap;
  }

  .error-banner button:hover {
    background: rgba(239, 83, 80, 0.3);
  }

  .centered {
    display: flex;
    justify-content: center;
    padding: 48px 0;
  }

  .muted {
    color: #555;
    font-size: 14px;
  }
</style>
