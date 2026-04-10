.PHONY: build dev test clean docker-build docker-up docker-down proto

# ── Build ─────────────────────────────────────────────────────────────────────
# Full production build: React dashboard → Go binary (static, embedded).
build:
	cd dashboard && npm run build
	mkdir -p bin
	go build -ldflags="-s -w" -o bin/flowgate ./cmd/flowgate

# ── Development ───────────────────────────────────────────────────────────────
# Vite dev server proxies /v1/* to the Go server.
# Kill with Ctrl+C; the background Go process is cleaned up by the shell.
dev:
	@echo "▶ Go server  → http://localhost:7700"
	@echo "▶ Dashboard  → http://localhost:5173/dashboard/"
	go run ./cmd/flowgate &
	cd dashboard && npm run dev

# ── Test ──────────────────────────────────────────────────────────────────────
# Exclude the Go file inside dashboard/node_modules that slips into ./...
test:
	go test $$(go list ./... | grep -v node_modules)

# ── Docker ────────────────────────────────────────────────────────────────────
docker-build:
	docker compose build

docker-up:
	docker compose up -d
	@echo "FlowGate running on http://localhost:7700"
	@echo "Dashboard at http://localhost:7700/dashboard"

docker-down:
	docker compose down

# ── Proto codegen ─────────────────────────────────────────────────────────────
proto:
	mkdir -p internal/grpc/pb
	protoc --go_out=internal/grpc/pb \
	       --go_opt=paths=source_relative \
	       --go-grpc_out=internal/grpc/pb \
	       --go-grpc_opt=paths=source_relative \
	       -I proto \
	       proto/flowgate.proto

# ── Clean ─────────────────────────────────────────────────────────────────────
clean:
	rm -rf bin/
	rm -rf internal/dashboard/dist/
	mkdir -p internal/dashboard/dist
	printf '<!DOCTYPE html><html><body style="background:#0f0f0f;color:#e0e0e0;font-family:monospace;padding:2rem"><p>Run <code>make build</code></p></body></html>' \
	  > internal/dashboard/dist/index.html
