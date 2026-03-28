<script>
  // Proto JSON encoding notes (grpc_json_transcoder with preserve_proto_field_names):
  //   - int64/uint64 fields (follow_count, total_count) are encoded as strings.
  //   - google.protobuf.Timestamp fields (created_at, first_follow, last_follow)
  //     are encoded as RFC3339 strings, which display fine as-is.
  /** @type {{ short_code: string, long_url: string, created_at: string, follow_count: string, first_follow: string, last_follow: string }[]} */
  let urls = $state([]);
  let nextPageToken = $state('');
  let totalCount = $state(0);
  let pageSize = $state(100);
  let loading = $state(true);
  let error = $state('');
  let currentToken = $state('');
  /** @type {string[]} */
  let pageHistory = $state([]);
  let currentPage = $state(1);

  async function load(pageToken = '', addToHistory = false) {
    if (addToHistory && currentToken !== '') {
      pageHistory = [...pageHistory, currentToken];
    }
    currentToken = pageToken;
    loading = true;
    error = '';
    try {
      const qs = pageToken ? `?page_token=${encodeURIComponent(pageToken)}&page_size=100` : '?page_size=100';
      const res = await fetch(`/admin/v0/urls${qs}`);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      urls = data.urls ?? [];
      nextPageToken = data.next_page_token ?? '';
      // total_count is a proto int64 — encoded as string by the transcoder.
      totalCount = Number(data.total_count ?? 0);
      pageSize = Number(data.page_size ?? 100);
      currentPage = pageHistory.length + 1;
    } catch (e) {
      error = /** @type {Error} */ (e).message;
    } finally {
      loading = false;
    }
  }

  function goNext() {
    load(nextPageToken, true);
  }

  function goPrev() {
    if (pageHistory.length === 0) return;
    const newHistory = [...pageHistory];
    const prevToken = newHistory.pop() ?? '';
    pageHistory = newHistory;
    currentToken = prevToken;
    load(prevToken, false);
  }

  function goFirst() {
    pageHistory = [];
    load('', false);
  }

  async function deleteURL(code) {
    if (!confirm(`Delete "${code}"?`)) return;
    const res = await fetch('/admin/v0/delete', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ short_code: code }),
    });
    if (res.ok) {
      load(currentToken, false);
    } else {
      const d = await res.json().catch(() => ({}));
      alert(`Delete failed: ${d.error ?? d.message ?? res.status}`);
    }
  }

  $effect(() => {
    load();
    const t = setInterval(() => load(currentToken, false), 30_000);
    return () => clearInterval(t);
  });

  const totalPages = $derived(Math.ceil(totalCount / pageSize) || 1);
</script>

<h2>Shortened URLs</h2>

{#if error}
  <p class="error">{error}</p>
{/if}

<div class="table-wrap">
  <table>
    <thead>
      <tr>
        <th>Code</th>
        <th>Long URL</th>
        <th>Created</th>
        <th class="r">Redirects</th>
        <th>First Redirect</th>
        <th>Last Redirect</th>
        <th></th>
      </tr>
    </thead>
    <tbody>
      {#if loading}
        <tr><td colspan="7" class="center muted">Loading…</td></tr>
      {:else if urls.length === 0}
        <tr><td colspan="7" class="center muted">No URLs found.</td></tr>
      {:else}
        {#each urls as u (u.short_code)}
          <tr>
            <td class="code">{u.short_code}</td>
            <td class="url" title={u.long_url}>{u.long_url}</td>
            <td class="ts">{u.created_at || '—'}</td>
            <td class="r">{u.follow_count}</td>
            <td class="ts">{u.first_follow || '—'}</td>
            <td class="ts">{u.last_follow || '—'}</td>
            <td><button class="del" onclick={() => deleteURL(u.short_code)}>Delete</button></td>
          </tr>
        {/each}
      {/if}
    </tbody>
  </table>
</div>

<div class="pager">
  <div class="pager-info">
    {totalCount.toLocaleString()} URLs total — Page {currentPage} of {totalPages}
  </div>
  <div class="pager-buttons">
    {#if pageHistory.length > 0}
      <button onclick={goFirst}>First</button>
      <button onclick={goPrev}>← Previous</button>
    {/if}
    {#if nextPageToken}
      <button onclick={goNext}>Next →</button>
    {/if}
  </div>
</div>

<style>
  h2 { font-size: 14px; color: #1e2a3a; margin-bottom: 10px; }

  .error {
    background: #fde8e8; color: #c0392b;
    padding: 10px 14px; border-radius: 4px; margin-bottom: 10px;
  }

  .table-wrap {
    background: #fff;
    border-radius: 5px;
    box-shadow: 0 1px 3px rgba(0,0,0,.1);
    overflow: auto;
  }

  table { width: 100%; border-collapse: collapse; }
  th {
    background: #eaecf0;
    text-align: left;
    padding: 8px 12px;
    font-size: 12px;
    font-weight: 600;
    border-bottom: 2px solid #d0d4db;
    white-space: nowrap;
  }
  td { padding: 7px 12px; border-bottom: 1px solid #eef0f3; vertical-align: middle; }
  tr:last-child td { border-bottom: none; }
  tr:hover td { background: #f8f9fb; }

  .code { font-family: monospace; color: #1a6a9a; }
  .url  { max-width: 300px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: #555; }
  .ts   { white-space: nowrap; color: #777; font-size: 12px; }
  .r    { text-align: right; }
  .center { text-align: center; }
  .muted  { color: #aaa; padding: 24px; }

  .del {
    background: #e74c3c; color: #fff;
    border: none; border-radius: 3px;
    padding: 3px 10px; font-size: 12px;
    cursor: pointer;
  }
  .del:hover { background: #c0392b; }

  .pager {
    margin-top: 12px;
    font-size: 12px;
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .pager-info {
    color: #666;
  }
  .pager-buttons {
    display: flex;
    gap: 8px;
  }
  .pager button {
    background: #f5f6f8;
    border: 1px solid #d0d4db;
    border-radius: 3px;
    padding: 4px 12px;
    color: #1a6a9a;
    cursor: pointer;
    font-size: 12px;
  }
  .pager button:hover {
    background: #e8eaed;
  }
</style>
