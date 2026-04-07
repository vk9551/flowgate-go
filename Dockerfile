# ── Stage 1: build React dashboard ───────────────────────────────────────────
FROM node:20-alpine AS node-build

WORKDIR /app

# Install deps before copying source so layer is cached on dep changes only.
COPY dashboard/package.json dashboard/package-lock.json ./dashboard/
RUN cd dashboard && npm ci --prefer-offline

# Copy dashboard source and build.
COPY dashboard/ ./dashboard/
COPY internal/dashboard/ ./internal/dashboard/
RUN cd dashboard && npm run build
# Output lands in internal/dashboard/dist/ per vite.config.ts outDir setting.


# ── Stage 2: build Go binary ──────────────────────────────────────────────────
FROM golang:1.25-alpine AS go-build

WORKDIR /app

# Copy all Go source including vendored deps (no network access needed).
COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY . .
COPY --from=node-build /app/internal/dashboard/dist/ ./internal/dashboard/dist/

# Build a static binary: no CGO (modernc/sqlite is pure Go), strip debug info.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -mod=vendor \
      -ldflags="-s -w" \
      -o /flowgate \
      ./cmd/flowgate


# ── Stage 3: minimal runtime image ───────────────────────────────────────────
FROM alpine:latest

# Create non-root user without needing apk (adduser/addgroup are in busybox).
RUN addgroup -S flowgate \
 && adduser  -S -G flowgate flowgate \
 && mkdir -p /data \
 && chown flowgate:flowgate /data

# Copy CA certificates from the Go build stage — avoids apk network dependency.
COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=go-build /flowgate /flowgate

USER flowgate

EXPOSE 7700

ENTRYPOINT ["/flowgate"]
# Default config path; override with FLOWGATE_CONFIG_PATH env var.
CMD ["/etc/flowgate/flowgate.yaml"]
