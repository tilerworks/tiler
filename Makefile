# Quality gates for the tiler development loop.
# A change is done when fmt, lint, and test are all green.

build: ## CGO_ENABLED=0 go build -o bin/ ./cmd/...
	CGO_ENABLED=0 go build -o bin/ ./cmd/...

fmt: ## golangci-lint fmt
	golangci-lint fmt

lint: ## golangci-lint run
	golangci-lint run

test: ## go test ./... -race -cover -timeout 5m
	go test ./... -race -cover -timeout 5m

# darwin-check and linux-check compile the other host's tests and do
# not run them. GOOS and GOARCH are set on these command lines only,
# so `make build` still writes bin/tiler for the machine running
# make. -o /dev/null discards the binaries.
darwin-check: ## cross-compile darwin tests for arm64 and amd64
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go test -c -o /dev/null ./...
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go test -c -o /dev/null ./...

linux-check: ## cross-compile linux tests for arm64 and amd64
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c -o /dev/null ./...
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o /dev/null ./...

cover: ## write coverage.out and open the HTML report
	go test ./... -coverprofile=coverage.out && go tool cover -html=coverage.out

e2e: build ## run shell e2e cases under test/e2e/ (skips cleanly without the toolchain)
	@set -e; \
	cd test/e2e; \
	ran=0; \
	for t in t[0-9][0-9][0-9][0-9]-*.sh; do \
		test -e "$$t" || continue; \
		ran=1; \
		echo "==> test/e2e/$$t"; \
		sh "$$t"; \
	done; \
	if test $$ran -eq 0; then \
		echo "e2e: no cases found under test/e2e/"; \
	fi

help: ## show available targets
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "%-8s %s\n", $$1, $$2}'

.PHONY: build fmt lint test darwin-check linux-check cover e2e help
