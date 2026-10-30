.PHONY: build ui ui-dev test test-full vet dev-db run

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

# The short suite, for each change (about 30 s): slow tests (jobs run with
# ansible-runner and a few others) are left to test-full.
test: vet
	go test -short ./...

# Everything, with the race detector (what full.yml runs nightly and before a
# milestone closes).
test-full: vet
	go test -race ./...

dev-db:
	./scripts/dev-db.sh

run: build
	AWXNEXT_DEV=1 ./bin/awxnext serve
