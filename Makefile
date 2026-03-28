## ============================================================
##  URL Shortener — Raft + Istio demo
##  Usage: make <target>
## ============================================================

NAMESPACE    ?= url-shortener
RELEASE      ?= url-shortener
IMAGE        ?= localhost/url-shortener:latest
INGRESS_HOST     ?= localhost
INGRESS_PORT     ?= 8080
ADMIN_HTTP_HOST  ?= localhost
ADMIN_HTTP_PORT  ?= 8082
DESCRIPTOR   := gen/descriptor/urlshortener.pb

# Colours
CYAN  := \033[36m
RESET := \033[0m

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ { printf "$(CYAN)%-22s$(RESET) %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.DEFAULT_GOAL := help

# ============================================================
#  Code generation
# ============================================================

.PHONY: generate
generate: ## buf generate + sqlc generate + proto descriptor + go mod tidy
	buf generate
	sqlc generate
	@mkdir -p gen/descriptor
	buf build -o $(DESCRIPTOR) --as-file-descriptor-set
	go mod tidy
	@echo "✓  generation complete"

# ============================================================
#  Local build
# ============================================================

.PHONY: build
build: ## Build the binary to bin/url-shortener
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/url-shortener .
	@echo "✓  bin/url-shortener"

# ============================================================
#  Docker
# ============================================================

ADMINUI_IMAGE ?= localhost/url-shortener-adminui:latest

.PHONY: docker-build
docker-build: ## Build the main service image for linux/arm64
	docker build \
		--platform linux/arm64 \
		-t $(IMAGE) \
		.
	@echo "✓  $(IMAGE)"

.PHONY: docker-build-amd64
docker-build-amd64: ## Build the main service image for linux/amd64
	docker build \
		--platform linux/amd64 \
		--build-arg TARGETARCH=amd64 \
		-t $(IMAGE) \
		.
	@echo "✓  $(IMAGE) (amd64)"

.PHONY: docker-build-adminui
docker-build-adminui: ## Build the SvelteKit admin UI image (nginx, build context: adminui/)
	docker build \
		--platform linux/arm64 \
		-t $(ADMINUI_IMAGE) \
		adminui/
	@echo "✓  $(ADMINUI_IMAGE)"

# ============================================================
#  Helm
# ============================================================

.PHONY: helm-install
helm-install: ## Deploy (or upgrade) the Helm release
	helm upgrade --install $(RELEASE) helm/url-shortener \
		--namespace $(NAMESPACE) \
		--create-namespace \
		--wait \
		--timeout 120s
	@echo "✓  Helm release '$(RELEASE)' installed in namespace '$(NAMESPACE)'"

.PHONY: helm-uninstall
helm-uninstall: ## Remove the Helm release (retains PVCs and namespace)
	helm uninstall $(RELEASE) --namespace $(NAMESPACE)

.PHONY: helm-template
helm-template: ## Render the Helm templates to stdout (dry-run)
	helm template $(RELEASE) helm/url-shortener \
		--namespace $(NAMESPACE)

.PHONY: helm-diff
helm-diff: ## Show pending Helm changes (requires helm-diff plugin)
	helm diff upgrade $(RELEASE) helm/url-shortener \
		--namespace $(NAMESPACE)

# ============================================================
#  Observability stack (Jaeger + OTEL Collector + Prometheus)
# ============================================================

.PHONY: otel-apply
otel-apply: ## Deploy Jaeger, OTEL Collector, Prometheus, and Grafana
	kubectl apply -f otel/jaeger.yaml
	kubectl apply -f otel/collector.yaml
	kubectl apply -f otel/prometheus.yaml
	kubectl apply -f otel/grafana.yaml
	@echo "✓  observability stack applied"

.PHONY: otel-remove
otel-remove: ## Remove the observability stack
	kubectl delete -f otel/grafana.yaml --ignore-not-found
	kubectl delete -f otel/prometheus.yaml --ignore-not-found
	kubectl delete -f otel/collector.yaml --ignore-not-found
	kubectl delete -f otel/jaeger.yaml --ignore-not-found

.PHONY: otel-wait
otel-wait: ## Wait for all observability pods to be ready
	kubectl rollout status deployment/jaeger        -n $(NAMESPACE) --timeout=60s
	kubectl rollout status deployment/otel-collector -n $(NAMESPACE) --timeout=60s
	kubectl rollout status deployment/prometheus     -n $(NAMESPACE) --timeout=60s
	kubectl rollout status deployment/grafana        -n $(NAMESPACE) --timeout=60s

# ============================================================
#  Istio service mesh
# ============================================================

.PHONY: envoyfilter-apply
envoyfilter-apply: ## Apply Envoy gRPC-JSON transcoder for URLShortenerService (port 80)
	@test -f $(DESCRIPTOR) || (echo "ERROR: $(DESCRIPTOR) not found — run 'make generate' first" && exit 1)
	@DESCRIPTOR_B64=$$(cat $(DESCRIPTOR) | base64 | tr -d '\n'); \
	sed "s|__DESCRIPTOR_B64__|$$DESCRIPTOR_B64|g" istio/envoy-filter-transcoder.yaml.tmpl \
		| kubectl apply -f -
	@echo "✓  EnvoyFilter (public) applied"

.PHONY: envoyfilter-admin-apply
envoyfilter-admin-apply: ## Apply Envoy gRPC-JSON transcoder for AdminService (port 8082)
	@test -f $(DESCRIPTOR) || (echo "ERROR: $(DESCRIPTOR) not found — run 'make generate' first" && exit 1)
	@DESCRIPTOR_B64=$$(cat $(DESCRIPTOR) | base64 | tr -d '\n'); \
	sed "s|__DESCRIPTOR_B64__|$$DESCRIPTOR_B64|g" istio/envoy-filter-admin-transcoder.yaml.tmpl \
		| kubectl apply -f -
	@echo "✓  EnvoyFilter (admin) applied"

.PHONY: istio-apply
istio-apply: envoyfilter-apply envoyfilter-admin-apply ## Apply Gateways, DestinationRules, VirtualServices, and EnvoyFilters
	kubectl apply -f istio/gateway-public.yaml
	kubectl apply -f istio/gateway-admin.yaml
	kubectl apply -f istio/destination-rules/
	kubectl apply -f istio/virtual-services/
	@echo "✓  Istio routing applied"

.PHONY: istio-remove
istio-remove: ## Remove all Istio routing config
	kubectl delete -f istio/virtual-services/ --ignore-not-found
	kubectl delete -f istio/destination-rules/ --ignore-not-found
	kubectl delete -f istio/gateway-admin.yaml --ignore-not-found
	kubectl delete -f istio/gateway-public.yaml --ignore-not-found
	kubectl delete envoyfilter/urlshortener-grpc-transcoder -n istio-system --ignore-not-found
	kubectl delete envoyfilter/urlshortener-admin-grpc-transcoder -n istio-system --ignore-not-found

# Patch the Istio ingressgateway Service to expose the admin gRPC port (9090).
# Run this once after Istio is installed.
.PHONY: istio-patch-admin-port
istio-patch-admin-port: ## Expose port 9090 on the istio-ingressgateway Service
	kubectl patch svc istio-ingressgateway -n istio-system \
		--type=json \
		-p='[{"op":"add","path":"/spec/ports/-","value":{"name":"grpc-admin","port":9090,"targetPort":9090,"protocol":"TCP"}}]'
	@echo "✓  ingressgateway now exposes port 9090"

# Patch the Istio ingressgateway Service to expose the admin UI HTTP port (8082).
# Run this once after Istio is installed.
.PHONY: istio-patch-adminui-port
istio-patch-adminui-port: ## Expose port 8082 on the istio-ingressgateway Service (admin UI)
	kubectl patch svc istio-ingressgateway -n istio-system \
		--type=json \
		-p='[{"op":"add","path":"/spec/ports/-","value":{"name":"http-admin-ui","port":8082,"targetPort":8082,"protocol":"TCP"}}]'
	@echo "✓  ingressgateway now exposes port 8082 (admin UI)"

# ============================================================
#  Full deploy (shortcut)
# ============================================================

.PHONY: deploy
deploy: docker-build docker-build-adminui helm-install otel-apply istio-apply ## Build images, deploy Helm, apply OTEL + Istio

# ============================================================
#  Port-forwarding
# ============================================================

# Local port mappings:
#   8080  →  url-shortener HTTP          (GET /:code redirect)
#   9092  →  url-shortener gRPC public   (ShortenURL, ResolveURL — no transcoding locally)
#   8082  →  Istio ingressgateway:8082   (SvelteKit SPA + /admin/v0/ via grpc_json_transcoder)
#   16686 →  Jaeger UI
#   9091  →  Prometheus UI               (9090 inside cluster)
#   3000  →  Grafana UI

.PHONY: port-forward
port-forward: ## Forward app, admin UI (via Istio gateway), Jaeger, Prometheus, and Grafana
	@echo ""
	@echo "  App HTTP:        http://localhost:8080   (GET /{code} redirect)"
	@echo "  App gRPC public: localhost:9092          (ShortenURL, ResolveURL)"
	@echo "  Admin UI:        http://localhost:8082   (SvelteKit SPA + /admin/v0/ API)"
	@echo "  Jaeger UI:       http://localhost:16686"
	@echo "  Prometheus:      http://localhost:9091"
	@echo "  Grafana:         http://localhost:3000"
	@echo ""
	@echo "  Press Ctrl-C to stop all port-forwards"
	@echo ""
	@trap 'kill 0' INT TERM; \
	kubectl port-forward -n $(NAMESPACE)  svc/url-shortener     8080:8080   2>/dev/null & \
	kubectl port-forward -n $(NAMESPACE)  svc/url-shortener     9092:9092   2>/dev/null & \
	kubectl port-forward -n istio-system  svc/istio-ingressgateway 8082:8082 2>/dev/null & \
	kubectl port-forward -n $(NAMESPACE)  svc/jaeger            16686:16686 2>/dev/null & \
	kubectl port-forward -n $(NAMESPACE)  svc/prometheus         9091:9090  2>/dev/null & \
	kubectl port-forward -n $(NAMESPACE)  svc/grafana            3000:3000  2>/dev/null & \
	wait

# ============================================================
#  Debugging / introspection
# ============================================================

.PHONY: verify-sidecars
verify-sidecars: ## Show container counts per pod (expect 2/2 with Istio sidecar)
	@echo "Pod name → containers (should be 2 with istio-proxy sidecar)"
	@kubectl get pods -n $(NAMESPACE) \
		-o custom-columns='NAME:.metadata.name,READY:.status.containerStatuses[*].ready,CONTAINERS:.spec.containers[*].name'

.PHONY: logs
logs: ## Stream logs from all url-shortener pods
	kubectl logs -n $(NAMESPACE) \
		-l app=url-shortener \
		--all-containers=false \
		-f \
		--max-log-requests=10

.PHONY: pods
pods: ## Show pod status and raft-role labels
	kubectl get pods -n $(NAMESPACE) \
		-l app=url-shortener \
		-o custom-columns='NAME:.metadata.name,STATUS:.status.phase,RAFT_ROLE:.metadata.labels.raft-role,AGE:.metadata.creationTimestamp'

# ============================================================
#  Scaling
# ============================================================

.PHONY: scale-up
scale-up: ## Scale the StatefulSet to 5 replicas
	kubectl scale statefulset/$(RELEASE) \
		-n $(NAMESPACE) --replicas=5
	@echo "Scaled to 5 — watch 'make pods' for nodes to join the raft cluster"

.PHONY: scale-down
scale-down: ## Scale the StatefulSet back to 3 replicas
	kubectl scale statefulset/$(RELEASE) \
		-n $(NAMESPACE) --replicas=3

# ============================================================
#  Admin UI — local development
# ============================================================

.PHONY: adminui-dev
adminui-dev: ## Run SvelteKit dev server (proxies /admin/v0/ to localhost:8082 via Istio)
	@echo "Starting SvelteKit dev server — requires make port-forward in another terminal"
	@echo "  /admin/v0/ requests are proxied to localhost:8082 (Istio ingressgateway)"
	cd adminui && npm install && npm run dev

# ============================================================
#  Load testing  (requires wrk: brew install wrk)
#
#  scripts/create.lua  — POST /shorten only; measures write throughput
#  scripts/follow.lua  — GET /{code} only; fetches the full code corpus
#                        from the admin API before workers start, then
#                        hammers redirects. Istio routes to follower subset.
#
#  INGRESS_HOST / INGRESS_PORT must point to the Istio ingress gateway.
#  With Docker Desktop + Istio use the LoadBalancer external IP on port 80:
#    make loadtest-create INGRESS_HOST=<gw-ip> INGRESS_PORT=80
#
#  follow.lua reads the admin JSON API; defaults match make port-forward:
#    make loadtest-follow ADMIN_HTTP_HOST=<gw-ip> ADMIN_HTTP_PORT=8082
# ============================================================

.PHONY: loadtest-create
loadtest-create: ## POST /shorten load test — raw write throughput (4t / 50c / 30s)
	@echo "Create-only — http://$(INGRESS_HOST):$(INGRESS_PORT)/shorten"
	wrk -t4 -c50 -d30s -s scripts/create.lua http://$(INGRESS_HOST):$(INGRESS_PORT)

.PHONY: loadtest-follow
loadtest-follow: ## GET /{code} load test — pre-fetches all codes from admin API (4t / 50c / 30s)
	@echo "Follow-only — http://$(INGRESS_HOST):$(INGRESS_PORT)"
	ADMIN_HOST=$(ADMIN_HTTP_HOST) ADMIN_PORT=$(ADMIN_HTTP_PORT) \
	    wrk -t4 -c50 -d30s -s scripts/follow.lua http://$(INGRESS_HOST):$(INGRESS_PORT)

# ============================================================
#  Teardown
# ============================================================

.PHONY: clean
clean: ## Remove local build artefacts
	rm -rf bin/

.PHONY: teardown
teardown: istio-remove otel-remove helm-uninstall ## Remove everything except the namespace and PVCs
	@echo "✓  teardown complete (PVCs retained — delete manually if needed)"
