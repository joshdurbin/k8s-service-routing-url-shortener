<script>
  import { page } from '$app/state';
  let { children } = $props();
</script>

<header>
  <span class="brand">URL Shortener</span>
  <nav>
    <a href="/" class:active={page.url.pathname === '/'}>URLs</a>
    <a href="/cluster" class:active={page.url.pathname === '/cluster'}>Cluster</a>
  </nav>
  <span class="hint">auto-refresh 30s</span>
</header>

<main>
  {@render children()}
</main>

<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }

  :global(body) {
    font-family: "Helvetica Neue", Helvetica, Arial, sans-serif;
    font-size: 13px;
    background: #f4f5f7;
    color: #333;
  }

  header {
    background: #1e2a3a;
    color: #e0e6ed;
    height: 48px;
    padding: 0 20px;
    display: flex;
    align-items: center;
    gap: 28px;
  }

  .brand { font-size: 15px; font-weight: 600; letter-spacing: .3px; }

  nav { display: flex; gap: 4px; }
  nav a {
    color: #8fa8c8;
    text-decoration: none;
    padding: 6px 14px;
    border-radius: 4px;
    font-size: 13px;
    transition: background .15s, color .15s;
  }
  nav a:hover  { background: rgba(255,255,255,.08); color: #e0e6ed; }
  nav a.active { background: rgba(255,255,255,.13); color: #fff; font-weight: 600; }

  .hint { margin-left: auto; font-size: 11px; color: #5a7a9a; }

  main { padding: 20px; }
</style>
