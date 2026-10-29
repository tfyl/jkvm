VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
GOVULNCHECK ?= go run golang.org/x/vuln/cmd/govulncheck@v1.9.0
GORELEASER ?= go run github.com/goreleaser/goreleaser/v2@v2.19.0

.PHONY: build test lint fmt vuln check e2e snapshot completions tidy clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/jkvm ./cmd/jkvm

test:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run: make fmt"; exit 1; }
	go vet ./...
	go vet -tags e2e ./e2e/...
	go test -race -count=1 ./...

lint:
	$(GOLANGCI_LINT) run ./...

fmt:
	gofmt -w .

vuln:
	$(GOVULNCHECK) ./...

# check runs everything CI runs except the goreleaser build.
check: test lint vuln

# e2e runs the end-to-end tests against the device at JKVM_E2E_HOST. Set
# JKVM_E2E_INPUT=1 to include the tier that sends input; see CONTRIBUTING.md.
e2e:
	@test -n "$(JKVM_E2E_HOST)" || { echo "set JKVM_E2E_HOST, e.g. JKVM_E2E_HOST=http://jetkvm.local"; exit 2; }
	go test -tags e2e -count=1 -v ./e2e/...

# snapshot builds the release archives into dist/ without publishing.
snapshot:
	$(GORELEASER) release --snapshot --clean

# completions writes shell completion scripts to completions/ (goreleaser
# runs this before building the archives).
completions:
	mkdir -p completions
	for sh in bash zsh fish; do go run ./cmd/jkvm completion $$sh > completions/jkvm.$$sh; done

tidy:
	go mod tidy
	@git diff --exit-code -- go.mod go.sum || { echo "go.mod/go.sum not tidy: run go mod tidy"; exit 1; }

clean:
	rm -rf bin dist completions
