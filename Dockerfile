# syntax=docker/dockerfile:1

# ---- Stage 1: Build ----
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

WORKDIR /src

# Download dependencies first so this layer is cached independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

# Copy source.
COPY . .

# CGO_ENABLED=0: modernc.org/sqlite is pure Go — no C toolchain needed.
ARG TARGETOS=linux
ARG TARGETARCH=arm64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/url-shortener .

# ---- Stage 2: Runtime ----
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /out/url-shortener /usr/local/bin/url-shortener

EXPOSE 8080 8081 9090 9091 9092

USER nobody

ENTRYPOINT ["/usr/local/bin/url-shortener"]
CMD ["serve"]
