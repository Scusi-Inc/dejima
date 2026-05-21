GO            ?= go
GOFLAGS       ?=
LDFLAGS       ?= -s -w -X github.com/aoos/dejima/internal/version.Version=$(shell git describe --tags --dirty --always 2>/dev/null || echo dev)
BIN_DIR       ?= bin

IMAGE_NAME       ?= dejima/island:latest
IMAGE_REGISTRY   ?=
IMAGE_PLATFORMS  ?= linux/amd64,linux/arm64

PREFIX        ?= /usr/local
INSTALL_BIN   ?= $(PREFIX)/bin

.PHONY: all build dejima dejimad image image-multiarch install uninstall setup test lint fmt vet tidy clean

# One-shot bootstrap: checks Docker, builds binaries, installs, builds image, registers service.
setup:
	scripts/setup.sh

all: build

build: dejima dejimad

# Copy the built binaries to $PREFIX/bin. Uses sudo if the target isn't writeable.
install: build
	@mkdir -p $(INSTALL_BIN) 2>/dev/null || sudo mkdir -p $(INSTALL_BIN)
	@if [ -w $(INSTALL_BIN) ]; then \
		install -m 0755 $(BIN_DIR)/dejima $(INSTALL_BIN)/dejima; \
		install -m 0755 $(BIN_DIR)/dejimad $(INSTALL_BIN)/dejimad; \
	else \
		sudo install -m 0755 $(BIN_DIR)/dejima $(INSTALL_BIN)/dejima; \
		sudo install -m 0755 $(BIN_DIR)/dejimad $(INSTALL_BIN)/dejimad; \
	fi
	@echo "installed dejima + dejimad to $(INSTALL_BIN)"

uninstall:
	@if [ -w $(INSTALL_BIN) ]; then \
		rm -f $(INSTALL_BIN)/dejima $(INSTALL_BIN)/dejimad; \
	else \
		sudo rm -f $(INSTALL_BIN)/dejima $(INSTALL_BIN)/dejimad; \
	fi
	@echo "removed dejima + dejimad from $(INSTALL_BIN)"

# Build a native-arch image, loaded into the local Docker daemon. Fast for dogfood.
image:
	docker build -t $(IMAGE_NAME) -f image/Dockerfile .

# Build and push a multi-platform image. Requires `docker buildx` and a writeable
# registry. Set IMAGE_REGISTRY=ghcr.io/aoos or similar before running.
image-multiarch:
	@if [ -z "$(IMAGE_REGISTRY)" ]; then \
		echo "set IMAGE_REGISTRY to push a multi-arch image (e.g. IMAGE_REGISTRY=ghcr.io/aoos)"; \
		exit 1; \
	fi
	docker buildx build \
		--platform $(IMAGE_PLATFORMS) \
		-t $(IMAGE_REGISTRY)/island:latest \
		-f image/Dockerfile \
		--push .

dejima:
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/dejima ./cmd/dejima

dejimad:
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/dejimad ./cmd/dejimad

test:
	$(GO) test ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed; skipping"; exit 0; }
	golangci-lint run ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)
