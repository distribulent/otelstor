<script lang="ts">
  import { createEventDispatcher } from 'svelte'
  import type { ServiceBucket } from '../types.ts'

  export let service: ServiceBucket
  export let activeSpanService: string | null = null

  const dispatch = createEventDispatcher<{ showSpans: string; showTrace: string; deleteService: string }>()

  $: totalSpans = (service.months ?? []).reduce((sum, m) => sum + (m.span_count ?? 0), 0)
  $: maxSpans = Math.max(1, ...(service.months ?? []).map(m => m.span_count ?? 0))
  $: isActive = activeSpanService === service.name

  function formatCount(n: number): string {
    if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
    if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
    return String(n)
  }
</script>

<div class="card" class:active={isActive}>
  <div class="card-header">
    <span class="service-name">{service.name}</span>
  </div>
  <div class="card-actions">
    <span class="total-badge">{formatCount(totalSpans)} spans</span>
    <button
      class="drill-btn"
      class:drill-btn-active={isActive}
      on:click={() => dispatch('showSpans', service.name)}
    >
      {isActive ? '▲ Hide' : '▼ Drilldown'}
    </button>
    <button
      class="delete-btn"
      on:click={() => {
        if (window.confirm(`Delete all telemetry data for "${service.name}"? This cannot be undone.`)) {
          dispatch('deleteService', service.name)
        }
      }}
    >
      Delete
    </button>
  </div>

  {#if !service.months || service.months.length === 0}
    <p class="empty">No months recorded.</p>
  {:else}
    <table>
      <thead>
        <tr>
          <th>Month</th>
          <th class="right">Spans</th>
          <th class="bar-col"></th>
        </tr>
      </thead>
      <tbody>
        {#each service.months as m (m.month)}
          <tr>
            <td class="month">{m.month}</td>
            <td class="right count">{formatCount(m.span_count ?? 0)}</td>
            <td class="bar-col">
              <div class="bar-track">
                <div
                  class="bar-fill"
                  style="width: {((m.span_count ?? 0) / maxSpans) * 100}%"
                ></div>
              </div>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

<style>
  .card {
    background: #1a1a1a;
    border: 1px solid #2a2a2a;
    border-radius: 8px;
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 12px;
    transition: border-color 0.15s;
  }

  .card.active {
    border-color: rgba(77, 208, 196, 0.4);
  }

  .card-header {
    display: block;
  }

  .card-actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .service-name {
    font-size: 15px;
    font-weight: 600;
    color: #e0e0e0;
    word-break: break-all;
  }

  .total-badge {
    font-size: 12px;
    font-weight: 500;
    color: #4dd0c4;
    background: rgba(77, 208, 196, 0.1);
    border: 1px solid rgba(77, 208, 196, 0.25);
    border-radius: 12px;
    padding: 2px 10px;
    white-space: nowrap;
  }

  .drill-btn {
    font-size: 11px;
    font-weight: 500;
    color: #888;
    background: #222;
    border: 1px solid #333;
    border-radius: 6px;
    padding: 3px 10px;
    cursor: pointer;
    white-space: nowrap;
    transition: color 0.15s, border-color 0.15s, background 0.15s;
  }

  .drill-btn:hover {
    color: #ccc;
    border-color: #555;
  }

  .drill-btn-active {
    color: #4dd0c4;
    border-color: rgba(77, 208, 196, 0.4);
    background: rgba(77, 208, 196, 0.08);
  }

  .drill-btn-active:hover {
    color: #80e8e0;
    border-color: rgba(77, 208, 196, 0.6);
  }

  .delete-btn {
    font-size: 11px;
    font-weight: 500;
    color: #888;
    background: #222;
    border: 1px solid #333;
    border-radius: 6px;
    padding: 3px 10px;
    cursor: pointer;
    white-space: nowrap;
    transition: color 0.15s, border-color 0.15s, background 0.15s;
  }

  .delete-btn:hover {
    color: #ef5350;
    border-color: rgba(239, 83, 80, 0.5);
    background: rgba(239, 83, 80, 0.08);
  }

  table {
    width: 100%;
    border-collapse: collapse;
  }

  th {
    font-size: 11px;
    font-weight: 500;
    color: #666;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding: 0 0 6px;
    border-bottom: 1px solid #2a2a2a;
  }

  td {
    padding: 7px 0;
    border-bottom: 1px solid #1e1e1e;
    color: #c0c0c0;
    font-size: 13px;
  }

  tr:last-child td {
    border-bottom: none;
  }

  .right {
    text-align: right;
  }

  .month {
    font-family: monospace;
    color: #aaa;
  }

  .count {
    font-variant-numeric: tabular-nums;
    color: #e0e0e0;
    padding-right: 12px;
  }

  .bar-col {
    width: 120px;
    padding-left: 8px;
  }

  .bar-track {
    height: 6px;
    background: #2a2a2a;
    border-radius: 3px;
    overflow: hidden;
  }

  .bar-fill {
    height: 100%;
    background: linear-gradient(90deg, #4dd0c4, #2196f3);
    border-radius: 3px;
    transition: width 0.3s ease;
  }

  .empty {
    color: #555;
    font-size: 13px;
    text-align: center;
    padding: 12px 0;
  }
</style>
