# URL Shortener — Raft + Istio Demo

Per-pod SQLite replicated via Raft. Istio routes writes to leader, reads to followers.

**[TEACHING.md](TEACHING.md)** explains how everything works.

## Prerequisites

- Docker Desktop with Kubernetes (≥ 6 GB RAM)
- `brew install helm wrk`
- `go install github.com/bufbuild/buf/cmd/buf@latest`
- `go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest`
- `curl -L https://istio.io/downloadIstio | ISTIO_VERSION=1.29.1 sh -`

## Quick start

```bash
# First time only
istioctl install --set profile=default -y
make istio-patch-admin-port
make istio-patch-adminui-port

# Build and deploy
make deploy

# Access
make port-forward
```

| Port | Service |
|------|---------|
| 8080 | App HTTP |
| 8082 | Admin UI |
| 16686 | Jaeger |
| 9091 | Prometheus |

## Usage

```bash
# Shorten
curl -X POST http://localhost:8080/shorten \
  -H "Content-Type: application/json" \
  -d '{"long_url":"https://example.com"}'

# Resolve
curl -L http://localhost:8080/<code>
```

## Operations

```bash
make pods           # Show raft roles
make scale-up       # 3 → 5 replicas
make scale-down     # 5 → 3 replicas
make logs           # Stream logs
make loadtest-create
make loadtest-follow
make teardown
```
