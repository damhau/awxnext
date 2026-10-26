.PHONY: build test vet dev-db run

build:
	go build -o bin/awxnext ./cmd/awxnext

vet:
	go vet ./...

test: vet
	go test ./...

dev-db:
	./scripts/dev-db.sh

run: build
	AWXNEXT_DEV=1 ./bin/awxnext serve
