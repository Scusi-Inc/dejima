GO            ?= go
GOFLAGS       ?=
# VERSION drives the baked-in build version. Defaults to a git-describe string;
# release CI overrides it with the tag (e.g. VERSION=v0.1.0).
VERSION       ?= $(shell git describe --tags --dirty --always 2>/dev/null || echo dev)
LDFLAGS       ?= -s -w -X github.com/aoos/dejima/internal/version.Version=$(VERSION)
BIN_DIR       ?= bin

# When set (release CI on macOS), release-binaries codesigns the darwin binaries
# with this Developer ID identity, e.g. "Developer ID Application: Name (TEAMID)".
# Empty = unsigned. See docs/release-notarization.md.
CODESIGN_IDENTITY ?=

IMAGE_NAME       ?= dejima/island:latest
IMAGE_REGISTRY   ?=
IMAGE_PLATFORMS  ?= linux/amd64,linux/arm64

PREFIX        ?= /usr/local
INSTALL_BIN   ?= $(PREFIX)/bin

.PHONY: all build dejima dejimad image image-multiarch install uninstall setup client-binaries release-binaries test test-integration test-tier3-safe test-tier3-system test-tier3-action-gate test-tier3-reconnect test-tier3-tui-claude test-tier3-onboard-selftest test-tier4 lint fmt vet tidy clean

# One-shot bootstrap: checks Docker, builds binaries, installs, builds image, registers service.
setup:
	scripts/setup.sh

# Cross-compile the CLI (client) for every supported platform into dist/.
# The CLI is a pure client — it builds and runs anywhere (Windows / macOS /
# Linux); only the dejimad daemon needs a Unix host with Docker.
client-binaries:
	@mkdir -p dist
	GOOS=darwin  GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/dejima-darwin-arm64        ./cmd/dejima
	GOOS=darwin  GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/dejima-darwin-amd64        ./cmd/dejima
	GOOS=linux   GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/dejima-linux-arm64         ./cmd/dejima
	GOOS=linux   GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/dejima-linux-amd64         ./cmd/dejima
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/dejima-windows-amd64.exe   ./cmd/dejima
	GOOS=windows GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/dejima-windows-arm64.exe   ./cmd/dejima
	@echo "client binaries in dist/"

# Build packaged release archives for every target into dist/. Unix archives
# carry both `dejima` and `dejimad` (the daemon only runs on Unix hosts);
# Windows archives carry the client only. Consumed by the release CI workflow.
# Override VERSION in CI: `make release-binaries VERSION=v0.1.0`.
release-binaries:
	@mkdir -p dist
	@rm -f dist/dejima_*.tar.gz dist/dejima_*.zip dist/SHA256SUMS
	@set -e; ver="$(VERSION)"; \
	for t in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do \
	  os=$${t%/*}; arch=$${t#*/}; d=$$(mktemp -d); \
	  echo "  building $$os/$$arch (dejima + dejimad)"; \
	  GOOS=$$os GOARCH=$$arch $(GO) build -ldflags "$(LDFLAGS)" -o $$d/dejima  ./cmd/dejima; \
	  GOOS=$$os GOARCH=$$arch $(GO) build -ldflags "$(LDFLAGS)" -o $$d/dejimad ./cmd/dejimad; \
	  if [ "$$os" = darwin ] && [ -n "$(CODESIGN_IDENTITY)" ]; then \
	    echo "  codesigning darwin/$$arch"; \
	    codesign --force --options runtime --timestamp --sign "$(CODESIGN_IDENTITY)" $$d/dejima $$d/dejimad; \
	  fi; \
	  cp LICENSE README.md $$d/ 2>/dev/null || true; \
	  tar -czf dist/dejima_$${ver}_$${os}_$${arch}.tar.gz -C $$d . ; \
	  rm -rf $$d; \
	done; \
	for arch in amd64 arm64; do \
	  d=$$(mktemp -d); \
	  echo "  building windows/$$arch (dejima only)"; \
	  GOOS=windows GOARCH=$$arch $(GO) build -ldflags "$(LDFLAGS)" -o $$d/dejima.exe ./cmd/dejima; \
	  cp LICENSE README.md $$d/ 2>/dev/null || true; \
	  ( cd $$d && zip -q dejima_$${ver}_windows_$${arch}.zip dejima.exe LICENSE README.md ); \
	  mv $$d/dejima_$${ver}_windows_$${arch}.zip dist/ ; \
	  rm -rf $$d; \
	done; \
	( cd dist && shasum -a 256 dejima_$${ver}_* > SHA256SUMS ) ; \
	echo "release archives + SHA256SUMS in dist/"

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

# test-integration runs the DETERMINISTIC FULL-FEATURE Tier-2 suite against a
# LIVE Docker host — one dispatch exercises every feature once with per-feature
# pass/fail: lifecycle, Port, MCP, multi-agent, clone, inter-island, the purge
# guard, capability brokering, provider creds, token roles, webhooks, activity,
# panic, and doctor. Set DEJIMA_REPORT=<path> to also emit the JSON summary
# (pass/fail per feature) for the CI artifact + issue-on-failure step. Requires
# docker (running), go, git. Runs in a throwaway $HOME; purges islands + daemon.
test-integration:
	./scripts/integration.sh

# Tier-3 macOS-host suite — run as the `dejimaqa` test user on the Mac-mini
# self-hosted runner (its OWN dejimad + colima, never the operator's). The safe
# target mutates nothing on the host (Keychain secret storage, idle hibernate,
# terminal reconnect, SSH-façade plumbing). The system target installs a
# system-wide LaunchDaemon / runs onboard --provision-host and is OPT-IN only —
# it no-ops unless DEJIMA_RUN_SYSTEM=1 (and never reboots unless DEJIMA_RUN_REBOOT=1).
# See docs/testing/dejimaqa-runner-setup.md.
test-tier3-safe:
	./scripts/tier3/safe.sh

test-tier3-system:
	./scripts/tier3/system.sh

# Tier-3 ACTION-GATE — live, end-to-end proof of the cross-island action gate's
# POLICY DELTA against a real daemon + two real islands (deny-all, exposed→queue,
# counted auto-approve within budget then re-queue, destructive-always-queues,
# policy.add/remove + auto link.approve ledgered, queue dropped on restart). Safe
# (throwaway $HOME/daemon, never aoos's); island-backed so it SKIPS cleanly when
# colima/Docker is unreachable. See scripts/tier3/action-gate.sh.
test-tier3-action-gate:
	./scripts/tier3/action-gate.sh

# Tier-3 RECONNECT-RESILIENCE — the live proof of #129: an attached `dejima shell`
# session SURVIVES a daemon restart with no code-1 exit and resumes the same
# in-container tmux session, a clean stdin close exits 0, and a genuinely-gone
# target gives up fast with a clear message. Safe (throwaway $HOME/daemon, never
# aoos's); island-backed so it SKIPS cleanly without colima. See
# scripts/tier3/reconnect.sh.
test-tier3-reconnect:
	./scripts/tier3/reconnect.sh

# Phase-C2 live UX checks (Mac-mini runner). Both SKIP cleanly without their
# gates so they never red a partially-provisioned runner.
# - tui-claude: walks the real TUI in tmux and has Claude judge each frame.
#   Needs TEST_AGENT_KEY (+ tmux). See scripts/tier3/tui-claude.sh.
# - onboard-selftest: times `onboard --provision-host --yes` (asserts < 5 min,
#   idempotent) and has Claude critique the setup UX. Mutating, so gated behind
#   DEJIMA_RUN_SYSTEM=1; the Claude critique needs TEST_AGENT_KEY.
test-tier3-tui-claude:
	./scripts/tier3/tui-claude.sh

test-tier3-onboard-selftest:
	./scripts/tier3/onboard-selftest.sh

# Tier-4 real-agent smoke — launches a REAL agent and asserts it ran / produced
# a commit (never exact LLM output). Secret-gated: no-ops with a notice unless
# TEST_AGENT_KEY is set; pushes to the bot repo when TEST_GH_TOKEN/TEST_GH_OWNER
# /TEST_GH_REPO are present.
test-tier4:
	./scripts/tier4/agent-smoke.sh

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
