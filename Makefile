.PHONY: build ui ui-dev test vet dev-db run

build:
	go build -o bin/awxnext ./cmd/awxnext

# Build the web UI into ui/dist (embedded by the next `make build`).
ui:
	cd ui && npm ci && npm run build

# UI dev server with hot reload, proxying the API to AWXNEXT_URL (default :8052).
ui-dev:
	cd ui && npm run dev

vet:
	go vet ./...

test: vet
	go test ./...

dev-db:
	./scripts/dev-db.sh

run: build
	AWXNEXT_DEV=1 ./bin/awxnext serve
