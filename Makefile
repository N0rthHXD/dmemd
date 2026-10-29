GO ?= go
GOFMT ?= $(shell $(GO) env GOROOT)/bin/gofmt
NPM ?= npm
WEB_DIR ?= web
VERSION ?= dev

# node_modules can contain unrelated .go files; keep Go checks scoped to this module.
GO_PACKAGES = $(shell $(GO) list ./... | grep -v '/web/node_modules/')

.PHONY: all fmt-check vet test test-integration test-quality test-capacity test-race build check clean \
	web-install web-dev web-check web-build build-ui

all: check

fmt-check:
	@unformatted="$$($(GOFMT) -l $$(find . -type f -name '*.go' -not -path './.git/*' -not -path './web/node_modules/*'))"; \
	if [ -n "$$unformatted" ]; then \
		echo "以下 Go 文件需要 gofmt："; \
		echo "$$unformatted"; \
		exit 1; \
	fi

vet:
	$(GO) vet $(GO_PACKAGES)

test:
	$(GO) test $(GO_PACKAGES)

test-integration:
	$(GO) test -count=1 ./tests/integration/...

test-quality:
	@files="$$($(GO) list -f '{{join .GoFiles " "}}' ./tests/quality 2>/dev/null)"; \
	if [ -n "$$files" ]; then \
		$(GO) test -count=1 -v ./tests/quality/...; \
	else \
		echo "no active quality test packages (legacy-tagged suite skipped)"; \
	fi

# Capacity is opt-in: the default suite stays fast and does not seed 10k rows.
test-capacity:
	DMEMD_CAPACITY=1 $(GO) test -count=1 -timeout=30m -v ./tests/capacity/...

test-race:
	CGO_ENABLED=1 $(GO) test -race $(GO_PACKAGES)

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/dmemd ./cmd/dmemd

check: fmt-check vet test test-quality build

# Frontend dependencies are installed explicitly; normal Go builds never call npm.
web-install:
	$(NPM) --prefix $(WEB_DIR) ci

web-dev:
	$(NPM) --prefix $(WEB_DIR) run dev

web-check:
	$(NPM) --prefix $(WEB_DIR) run check

web-build:
	$(NPM) --prefix $(WEB_DIR) run build

# build-ui and build intentionally write the same binary path.
build-ui: web-build
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -tags webui -ldflags "-s -w -X main.version=$(VERSION)" -o bin/dmemd ./cmd/dmemd

clean:
	rm -rf bin coverage.out coverage.html
