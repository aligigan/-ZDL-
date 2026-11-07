BINARY  := zdl
VERSION ?= 0.2.0
OUT     := dist
LDFLAGS := -s -w -X main.version=$(VERSION)

GO      ?= go
GOFLAGS := -trimpath

.PHONY: all build install test vet fmt tidy clean release aur

all: build

# Local build for the current platform, installed on PATH.
build:
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/zdl

install:
	mkdir -p $(HOME)/.local/bin
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(HOME)/.local/bin/$(BINARY) ./cmd/zdl

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w cmd internal testserver

tidy:
	$(GO) mod tidy
	$(GO) mod vendor
	$(GO) mod verify

# Cross-compiled Linux archives (amd64, arm64) + SHA256SUMS.
release:
	./scripts/release.sh "$(VERSION)"

# Refresh the AUR zdl-bin package after a release.
aur:
	./scripts/aur-update.sh "$(VERSION)"

clean:
	rm -rf $(BINARY) $(OUT)
