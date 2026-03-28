<script>
  // Proto JSON encoding note: uint64 fields (term, commit_index, last_index) are
  // encoded as strings by grpc_json_transcoder. Template interpolation ({cluster.term})
  // works fine; arithmetic would require Number() conversion.
  /** @type {{ leader: string, state: string, term: string, commit_index: string, last_index: string, peers: {id:string,address:string,suffrage:string}[] } | null} */
  let cluster = $state(null);
  let loading = $state(true);
  let error = $state('');
  let electing = $state(false);

  async function load() {
    loading = true;
    error = '';
    try {
      const res = await fetch('/admin/v0/cluster');
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      cluster = await res.json();
    } catch (e) {
      error = /** @type {Error} */ (e).message;
    } finally {
      loading = false;
    }
  }

  async function triggerElection() {
    if (!confirm('Force a raft leader election now?')) return;
    electing = true;
    try {
      const res = await fetch('/admin/v0/trigger-election', { method: 'POST' });
      const data = await res.json().catch(() => ({}));
      if (!res.ok || !data.ok) throw new Error(data.error ?? data.message ?? `HTTP ${res.status}`);
      setTimeout(load, 1500);
    } catch (e) {
      alert(`Trigger election failed: ${/** @type {Error} */ (e).message}`);
    } finally {
      electing = false;
    }
  }

  $effect(() => {
    load();
    const t = setInterval(load, 30_000);
    return () => clearInterval(t);
  });

  const stateClass = $derived(
    cluster?.state === 'Leader'    ? 'leader'    :
    cluster?.state === 'Candidate' ? 'candidate' : 'follower'
  );
</script>

<h2>Cluster State</h2>

{#if error}
  <p class="error">{error}</p>
{/if}

{#if loading && !cluster}
  <p class="muted">Loading…</p>
{:else if cluster}
  <div class="cards">
    <div class="card">
      <div class="label">State</div>
      <div class="value {stateClass}">{cluster.state}</div>
    </div>
    <div class="card">
      <div class="label">Term</div>
      <div class="value">{cluster.term}</div>
    </div>
    <div class="card">
      <div class="label">Commit Index</div>
      <div class="value">{cluster.commit_index}</div>
    </div>
    <div class="card">
      <div class="label">Last Index</div>
      <div class="value">{cluster.last_index}</div>
    </div>
  </div>

  <p class="section-label">Leader</p>
  <p class="mono mb">{cluster.leader || '(unknown)'}</p>

  <p class="section-label">Members</p>
  <div class="table-wrap">
    <table>
      <thead>
        <tr><th>Raft Address</th><th>Suffrage</th></tr>
      </thead>
      <tbody>
        {#each cluster.peers as p (p.id)}
          <tr>
            <td class="mono">{p.address}</td>
            <td>{p.suffrage}</td>
          </tr>
        {:else}
          <tr><td colspan="2" class="center muted">No peers.</td></tr>
        {/each}
      </tbody>
    </table>
  </div>

  <div class="actions">
    <button class="danger" onclick={triggerElection} disabled={electing}>
      {electing ? 'Triggering…' : 'Trigger Election'}
    </button>
  </div>
{/if}

<style>
  h2 { font-size: 14px; color: #1e2a3a; margin-bottom: 14px; }
  .error { background: #fde8e8; color: #c0392b; padding: 10px 14px; border-radius: 4px; margin-bottom: 12px; }
  .muted { color: #aaa; padding: 8px 0; }

  .cards { display: flex; gap: 12px; flex-wrap: wrap; margin-bottom: 20px; }
  .card {
    background: #fff;
    border-radius: 5px;
    padding: 12px 18px;
    box-shadow: 0 1px 3px rgba(0,0,0,.1);
    min-width: 140px;
  }
  .label { font-size: 10px; color: #999; text-transform: uppercase; letter-spacing: .6px; margin-bottom: 4px; }
  .value { font-size: 22px; font-weight: 700; font-family: monospace; color: #1e2a3a; }
  .value.leader    { color: #27ae60; }
  .value.follower  { color: #2980b9; }
  .value.candidate { color: #e67e22; }

  .section-label { font-size: 12px; font-weight: 600; color: #666; text-transform: uppercase; letter-spacing: .5px; margin-bottom: 6px; }
  .mono { font-family: monospace; color: #555; }
  .mb   { margin-bottom: 20px; }

  .table-wrap {
    background: #fff;
    border-radius: 5px;
    box-shadow: 0 1px 3px rgba(0,0,0,.1);
    overflow: auto;
    margin-bottom: 20px;
  }
  table { width: 100%; border-collapse: collapse; }
  th {
    background: #eaecf0;
    text-align: left;
    padding: 8px 12px;
    font-size: 12px;
    font-weight: 600;
    border-bottom: 2px solid #d0d4db;
  }
  td { padding: 7px 12px; border-bottom: 1px solid #eef0f3; }
  tr:last-child td { border-bottom: none; }
  .center { text-align: center; }

  .actions { margin-top: 4px; }
  .danger {
    background: #c0392b; color: #fff;
    border: none; border-radius: 3px;
    padding: 8px 18px; font-size: 13px; font-weight: 500;
    cursor: pointer;
    transition: background .15s;
  }
  .danger:hover:not(:disabled) { background: #a93226; }
  .danger:disabled { opacity: .6; cursor: default; }
</style>
