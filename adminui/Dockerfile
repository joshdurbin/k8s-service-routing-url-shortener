# Admin UI — SvelteKit static build served by nginx.
#
# Build context must be this directory (adminui/):
#   docker build -t localhost/url-shortener-adminui:latest adminui/
#
# Or from the repo root via make:
#   make docker-build-adminui

# ---- Stage 1: build ----
FROM node:22-alpine AS builder
WORKDIR /app
COPY package.json package-lock.json* ./
# Use npm install if no lock file is present; npm ci once package-lock.json is committed.
RUN npm install --prefer-offline 2>/dev/null || npm install
COPY . .
RUN npm run build

# ---- Stage 2: serve ----
FROM nginx:1.27-alpine
COPY --from=builder /app/build /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
