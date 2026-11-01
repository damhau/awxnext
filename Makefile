.PHONY: build ui ui-dev test test-full vet dev-db run images vuln vuln-go vuln-web vuln-images sbom pinned-actions

# Vulnerability scanners, each pinned (Trivy's image by digest).
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0
TRIVY       := aquasec/trivy:0.76.1@sha256:790fd66d1697395d8693d1acaa1ea702406a20f109b6fc7bac8db623eca6c06c
# The images vuln-images and sbom build and scan: awxnext, then the all-in-one.
IMAGES      ?= awxnext:scan awxnext-all-in-one:scan
# Trivy, without telemetry: it reads the local images through Docker, keeps its
# database in a volume between runs, and skips the findings accepted in
# .trivyignore.
TRIVY_ENV    = -e TRIVY_DISABLE_TELEMETRY=true -e TRIVY_SKIP_VERSION_CHECK=true
TRIVY_RUN    = docker run --rm $(TRIVY_ENV) -v /var/run/docker.sock:/var/run/docker.sock \
	-v awxnext-trivy-cache:/root/.cache/trivy -v $(CURDIR)/.trivyignore:/trivyignore:ro $(TRIVY)

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

# Known vulnerabilities (security.yml; docs/operations.md, "Security scanning"):
# the Go code and the UI's dependencies on the pushes to main that change them
# and daily, the images and SBOMs daily and by hand.
vuln: vuln-go vuln-web vuln-images

# What the Go code reaches, with the Go that builds it: GOTOOLCHAIN keeps
# `go run` from switching to a newer toolchain, whose standard library
# govulncheck would then check instead.
vuln-go:
	GOTOOLCHAIN=$$(go env GOVERSION) go run $(GOVULNCHECK) ./...

# The UI's npm dependencies, its build and test tools included.
vuln-web:
	cd ui && npm audit

# The Dockerfile's two images: awxnext (the default target), then the all-in-one.
images:
	docker build -t $(word 1,$(IMAGES)) .
	docker build --target all-in-one -t $(word 2,$(IMAGES)) .

# Both images: no HIGH or CRITICAL vulnerability that has a fix. Each image is
# scanned, then the target fails if either has one.
vuln-images: images
	rc=0; for i in $(IMAGES); do \
		$(TRIVY_RUN) image --quiet --scanners vuln --ignorefile /trivyignore \
			--severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 $$i || rc=1; \
	done; exit $$rc

# CycloneDX SBOMs in ./sbom: each image (its OS, Python and Go packages), and
# the UI's npm dependencies, which its image holds only as built files.
sbom: images
	mkdir -p sbom
	for i in $(IMAGES); do \
		$(TRIVY_RUN) image --quiet --format cyclonedx $$i \
			> sbom/$$(echo $$i | sed 's|.*/||; s|:.*||').cdx.json || exit 1; \
	done
	docker run --rm $(TRIVY_ENV) -v $(CURDIR)/ui/package.json:/ui/package.json:ro \
		-v $(CURDIR)/ui/package-lock.json:/ui/package-lock.json:ro \
		$(TRIVY) fs --quiet --format cyclonedx /ui > sbom/awxnext-ui-npm.cdx.json

# Every action the workflows use is pinned to a commit, with its version in a
# comment (`uses: actions/checkout@<commit> # v4.4.0`), since whoever controls
# an action can move its tags; Dependabot keeps the pins current. Local
# workflows (./.github/workflows/...) are part of this repository. ci.yml runs
# this check.
pinned-actions:
	@unpinned=$$(grep -rnE '^[[:space:]]*(-[[:space:]]+)?uses:' .github/workflows \
		| grep -vE 'uses:[[:space:]]+(\./|[^@[:space:]]+@[0-9a-f]{40}([[:space:]]|$$))'); \
	if [ -n "$$unpinned" ]; then \
		echo "Pin these actions to a commit, with the version in a comment:"; \
		echo "$$unpinned"; exit 1; \
	fi
