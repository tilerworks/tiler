# Quality gates for the tiler development loop.
# A change is done when fmt, lint, and test are all green.

build: ## CGO_ENABLED=0 go build -o bin/ ./cmd/...
	CGO_ENABLED=0 go build -o bin/ ./cmd/...

fmt: ## golangci-lint fmt
	golangci-lint fmt

lint: ## golangci-lint run
	golangci-lint run

test: ## go test ./... -race -cover
	go test ./... -race -cover

cover: ## write coverage.out and open the HTML report
	go test ./... -coverprofile=coverage.out && go tool cover -html=coverage.out

help: ## show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "%-8s %s\n", $$1, $$2}'

.PHONY: build fmt lint test cover help
