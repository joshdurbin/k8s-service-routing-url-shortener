# URL Shortener — Raft + Istio Demo

Single Go binary. Per-pod SQLite replicated via Hashicorp Raft. Deployed on Kubernetes as a StatefulSet with Istio for leader-aware HTTP routing.

## Key commands

```bash
make build                # compile → bin/url-shortener
make generate             # buf generate + sqlc generate + go mod tidy
make docker-build         # linux/arm64 image (Docker Desktop)
make docker-build-amd64
make docker-build-adminui # build SvelteKit nginx image
make deploy               # docker-build + docker-build-adminui + helm-install + otel-apply + istio-apply
make port-forward         # 8080 HTTP, 16686 Jaeger, 9091 Prometheus, 19090 admin gRPC, 8082 admin UI
make adminui-dev          # local SvelteKit dev server (proxies /admin/v0/ to :8083)
make loadtest             # wrk mixed create+follow (requires Istio ingress)
make loadtest-create      # wrk create-only
make trigger-election
make scale-up / scale-down
make teardown
```

## Project layout

```
cmd/            serve + admin + admin-ui subcommands (Cobra/Viper)
internal/
  adminui/      JSON API server for the SvelteKit admin UI
  db/           sqlc-generated layer + goose migrations
  raftcluster/  Hashicorp Raft cluster: bootstrap, join, FSM, peer reconciliation
  service/      HTTP handlers (shortener) + gRPC AdminService + follow stats
  shortcode/    Deterministic 8-char base62 encoder
pkg/            Config, logging, tracing, metrics
proto/          .proto sources (urlshortener/v1 gRPC-transcoded + admin/v1 gRPC-only)
gen/go/         Generated proto code — do not edit by hand
adminui/        SvelteKit SPA + nginx Dockerfile (own container image)
helm/           StatefulSet chart (replicaCount, volumeClaimTemplates, RBAC)
istio/          Gateways, DestinationRules, VirtualServices
otel/           Jaeger + OTEL Collector + Prometheus manifests
scripts/        wrk Lua load test scripts
```

## Architecture

- **SQLite** (`modernc.org/sqlite`) — pure Go, no CGO, one per pod. WAL mode, `MaxOpenConns=1`.
- **Hashicorp Raft** — BoltDB for log/stable stores, SQLite FSM, file snapshot store.
- **Counter block reservation** — leader reserves `[N, N+blockSize)` via `CmdReserveBlock`. Block resets to 0 on leader change; new leader reserves fresh block on first write.
- **Short code encoding** — `counter XOR xorKey → multiply by Knuth constant (6364136223846793005) → mod 62^8 → base62 (8 chars)`. See `internal/shortcode/encode.go`.
- **Follow stats** — On each redirect, a `RecordFollow` gRPC call is made asynchronously (fire-and-forget goroutine). Istio mesh routing directs the call to the leader, which applies it via raft.
- **Istio routing** — pod label `raft-role: leader|follower` patched on leadership change. `POST /shorten` → leader subset; `GET /{code}` → follower subset.
- **Peer discovery** — K8s EndpointSlices API (`discovery.k8s.io/v1`, primary), DNS ordinal fallback (`pod-{0..4}.headless-svc`). Leader reconciles every `RAFT_RECONCILE_INTERVAL` (default 15s).
- **Admin UI** — SvelteKit SPA (nginx, port 80) + Go JSON API (`admin-ui` subcommand, port 8083). nginx proxies `/admin/v0/` to the Go API. Exposed externally on port 8082 via Istio. Admin gRPC traffic is routed to the leader via Istio mesh routing (leader subset on `url-shortener-admin` service).

## FSM commands

| Constant        | Payload                      | Effect                        |
|-----------------|------------------------------|-------------------------------|
| CmdShortenURL   | `{short_code, long_url}`     | INSERT into urls              |
| CmdReserveBlock | `{new_counter_value}`        | UPSERT counter                |
| CmdRecordFollow | `{short_code, at_unix}`      | UPSERT url_stats              |
| CmdDeleteURL    | `{short_code}`               | DELETE from urls + url_stats  |

## Generated code — do not edit

- `gen/go/admin/v1/*.go` — regenerate with `buf generate`
- `internal/db/*.go` (models.go, query.sql.go, db.go) — regenerate with `sqlc generate`

`sqlc generate` requires `sqlc.yaml`. The generated `InsertURL` takes `InsertURLParams` struct (not positional args). `ListURLs` pagination uses `ListURLsParams.ID` as the cursor (items with `id > cursor`).

Migrations are embedded via `//go:embed migrations/*.sql` in `internal/db/migrations.go` and run with `goose.SetBaseFS(db.MigrationsFS); goose.Up(sqlDB, "migrations")`.

## Config (env vars / Viper)

All keys map to UPPER_SNAKE_CASE env vars automatically.

| Env var                    | Default                          | Notes                        |
|----------------------------|----------------------------------|------------------------------|
| HTTP_PORT                  | 8080                             |                              |
| GRPC_ADMIN_PORT            | 9090                             |                              |
| METRICS_PORT               | 8081                             |                              |
| RAFT_PORT                  | 9091                             |                              |
| RAFT_DATA_DIR              | /data/raft                       |                              |
| SQLITE_PATH                | /data/db.sqlite                  |                              |
| COUNTER_BLOCK_SIZE         | 100                              |                              |
| SHORT_CODE_XOR_KEY         | 0xdeadbeefcafebabe (as decimal)  | Must be decimal string in env |
| OTEL_ENDPOINT              | otel-collector:4317              |                              |
| LOG_LEVEL                  | info                             |                              |
| K8S_NAMESPACE              | url-shortener                    | Injected from metadata.namespace |
| K8S_HEADLESS_SERVICE       | urlshortener-headless            | Injected from Helm template  |
| POD_NAME                   | (empty)                          | Injected via Downward API    |
| RAFT_HEARTBEAT_TIMEOUT     | 1s                               |                              |
| RAFT_ELECTION_TIMEOUT      | 1s                               |                              |
| RAFT_COMMIT_TIMEOUT        | 50ms                             |                              |
| RAFT_MAX_APPEND_ENTRIES    | 64                               |                              |
| RAFT_TRAILING_LOGS         | 10240                            |                              |
| RAFT_SNAPSHOT_INTERVAL     | 120s                             |                              |
| RAFT_SNAPSHOT_THRESHOLD    | 8192                             |                              |
| RAFT_APPLY_TIMEOUT         | 10s                              | Apply, AddVoter, RemoveServer |
| RAFT_RECONCILE_INTERVAL    | 15s                              | Leader peer-reconcile loop   |
| RAFT_JOIN_RETRY_INTERVAL   | 3s                               |                              |
| RAFT_JOIN_MAX_RETRIES      | 30                               |                              |

## Helm

```bash
helm upgrade --install url-shortener helm/url-shortener --namespace url-shortener --create-namespace
```

StatefulSet name = release name (`url-shortener` with default `RELEASE`). Scale targets use `$(RELEASE)` not `$(RELEASE)-url-shortener`.

RBAC: Role in app namespace — `get/list/watch` EndpointSlices (`discovery.k8s.io/v1`), `get/patch` Pods.

## Ports

| Port | Name        | Purpose                                          |
|------|-------------|--------------------------------------------------|
| 8080 | http        | GET /{code} redirect, /healthz, /readyz          |
| 9092 | grpc-public | URLShortenerService gRPC (ShortenURL, ResolveURL)|
| 8081 | metrics     | /metrics, /healthz, /readyz                      |
| 9090 | grpc-admin  | AdminService gRPC                                |
| 9091 | raft        | Hashicorp Raft TCP transport                     |

Port 9092 is named `grpc-public` in the K8s Service — Istio treats `grpc-*` ports as HTTP/2 automatically.

Probes hit port 8081 (metrics). `/readyz` checks `db.PingContext`.

## Istio

Run these commands once after Istio install to expose admin ports on the ingressgateway Service:
```bash
make istio-patch-admin-port     # expose port 9090
make istio-patch-adminui-port   # expose port 8082
```

DestinationRule subsets: `leader` (`raft-role: leader`) and `follower` (`raft-role: follower`). VirtualService applies to both ingress and mesh gateways.

### gRPC-JSON transcoding (EnvoyFilter)

`POST /shorten` is transcoded from HTTP/JSON to gRPC `URLShortenerService.ShortenURL` by an Envoy filter on the ingress gateway (port 80 listener). `GET /{code}` has no `google.api.http` annotation so it passes through the filter unchanged → pod HTTP handler → 302 redirect.

The EnvoyFilter requires the proto `FileDescriptorSet` binary embedded as base64. The template is at `istio/envoy-filter-transcoder.yaml.tmpl`. Apply with:
```bash
make generate          # generates gen/descriptor/urlshortener.pb
make envoyfilter-apply # base64-encodes descriptor, renders template, kubectl apply
```

If the proto changes, re-run both commands. `make istio-apply` calls `envoyfilter-apply` automatically.

`PUBLIC_HOST` env var (default `localhost`) is embedded in `short_url` responses from `ShortenURL`. Set to the ingress gateway hostname in Helm values (`config.publicHost`).

## Notes

- `SHORT_CODE_XOR_KEY` is not in the StatefulSet env block by default — override via `extraEnv` in values.yaml if needed. Must be a decimal integer string, not hex.
- The headless service uses `publishNotReadyAddresses: true` so pods can discover each other via Endpoints before passing readiness checks.
- `GracefulStop()` on the gRPC server causes `Serve()` to return `nil`; any non-nil error from `Serve` is a genuine failure.
- The `ddd` file in the repo root is a scratch note — it is gitignored.
