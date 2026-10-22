GO ?= go
GOFMT ?= $(shell $(GO) env GOROOT)/bin/gofmt
VERSION ?= dev

.PHONY: all fmt-check vet test test-integration test-quality test-capacity test-race build check clean

all: check

fmt-check:
	@unformatted="$$($(GOFMT) -l $$(find . -type f -name '*.go' -not -path './.git/*'))"; \
	if [ -n "$$unformatted" ]; then \
		echo "以下 Go 文件需要 gofmt："; \
		echo "$$unformatted"; \
		exit 1; \
	fi

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-integration:
	$(GO) test -count=1 ./tests/integration/...

test-quality:
	$(GO) test -count=1 -v ./tests/quality/...

# Capacity is opt-in: the default suite stays fast and does not seed 10k rows.
test-capacity:
	DMEMD_CAPACITY=1 $(GO) test -count=1 -timeout=30m -v ./tests/capacity/...

test-race:
	CGO_ENABLED=1 $(GO) test -race ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/dmemd ./cmd/dmemd

check: fmt-check vet test test-quality build

clean:
	rm -rf bin coverage.out coverage.html
