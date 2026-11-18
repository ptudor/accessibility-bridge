# Accessibility Bridge control tools and protocol simulator.
GO        ?= go
BIN       := bin
PKG       := github.com/ptudor/accessibility-bridge/host
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -X $(PKG)/internal/buildinfo.Version=$(VERSION)
GOFLAGS   := -trimpath
BUILD_ENV := CGO_ENABLED=0

.PHONY: all build test test-network vet fmt tidy clean linux freebsd

all: build

build:
	mkdir -p $(BIN)
	$(BUILD_ENV) $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN)/ ./cmd/...

test: vet
	CGO_ENABLED=1 $(GO) test -race -count=1 ./...

# Optional real FFmpeg/MediaMTX test; all streams bind to loopback.
test-network:
	@test -n "$$HIDBRIDGE_TEST_MEDIAMTX" || { echo "set HIDBRIDGE_TEST_MEDIAMTX to the MediaMTX executable"; exit 1; }
	CGO_ENABLED=1 $(GO) test -race -count=1 -run '^TestNetworkSnapshots$$' -v ./internal/capture

vet:
	$(BUILD_ENV) $(GO) vet ./...
	GOOS=linux $(BUILD_ENV) $(GO) vet ./...
	GOOS=freebsd $(BUILD_ENV) $(GO) vet ./...

fmt:
	gofmt -l -w .

tidy:
	$(GO) mod tidy

linux:
	mkdir -p $(BIN)/linux-amd64 $(BIN)/linux-arm64
	GOOS=linux GOARCH=amd64 $(BUILD_ENV) $(GO) build $(GOFLAGS) -ldflags "-s -w $(LDFLAGS)" -o $(BIN)/linux-amd64/ ./cmd/...
	GOOS=linux GOARCH=arm64 $(BUILD_ENV) $(GO) build $(GOFLAGS) -ldflags "-s -w $(LDFLAGS)" -o $(BIN)/linux-arm64/ ./cmd/...

freebsd:
	mkdir -p $(BIN)/freebsd-amd64
	GOOS=freebsd GOARCH=amd64 $(BUILD_ENV) $(GO) build $(GOFLAGS) -ldflags "-s -w $(LDFLAGS)" -o $(BIN)/freebsd-amd64/ ./cmd/...

clean:
	rm -rf $(BIN)
