.PHONY: build build-cross build-android test test-router lint vet clean tidy install-browser

GO ?= go
GOFLAGS ?= -trimpath
LDFLAGS ?= -s -w
BUILD_DIR ?= dist
DESKTOP_TARGETS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
ANDROID_TARGETS ?= android/arm64

# Versioning baked at build time
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS += -X 'github.com/TONresistor/gocoon/pkg/cocoon.Version=$(VERSION)'
LDFLAGS += -X 'github.com/TONresistor/gocoon/pkg/cocoon.Commit=$(COMMIT)'
LDFLAGS += -X 'github.com/TONresistor/gocoon/pkg/cocoon.BuildDate=$(DATE)'

build:
	$(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/gocoon          ./cmd/gocoon
	$(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/gocoon-runner   ./cmd/gocoon-runner

build-cross:
	@for target in $(DESKTOP_TARGETS); do \
		goos=$${target%/*}; goarch=$${target#*/}; ext=""; \
		if [ "$$goos" = "windows" ]; then ext=".exe"; fi; \
		out="$(BUILD_DIR)/$$goos-$$goarch"; mkdir -p "$$out"; \
		echo "building $$target"; \
		CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch $(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o "$$out/gocoon$$ext" ./cmd/gocoon; \
		CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch $(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o "$$out/gocoon-runner$$ext" ./cmd/gocoon-runner; \
	done

build-android:
	@for target in $(ANDROID_TARGETS); do \
		goos=$${target%/*}; goarch=$${target#*/}; \
		out="$(BUILD_DIR)/$$goos-$$goarch"; mkdir -p "$$out"; \
		echo "building $$target"; \
		CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch $(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o "$$out/gocoon" ./cmd/gocoon; \
		CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch $(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o "$$out/gocoon-runner" ./cmd/gocoon-runner; \
	done

test:
	$(GO) test ./... -race -coverprofile=coverage.txt -covermode=atomic

test-router:
	$(GO) test -tags=router_integration ./pkg/router

vet:
	$(GO) vet ./...

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || echo "golangci-lint not installed, skipping"

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BUILD_DIR) coverage.txt coverage.html

# Copy gocoon-runner into the Tonnet Browser.
# Override BROWSER_REPO if not at the default sibling path.
BROWSER_REPO ?= ../Tonnet-Browser-stable
install-browser: build
	@test -d "$(BROWSER_REPO)/resources/bin/linux" || (echo "browser path not found: $(BROWSER_REPO)" && exit 1)
	cp $(BUILD_DIR)/gocoon $(BROWSER_REPO)/resources/bin/linux/gocoon
	cp $(BUILD_DIR)/gocoon-runner $(BROWSER_REPO)/resources/bin/linux/cocoon-runner
	@echo "installed gocoon and gocoon-runner into $(BROWSER_REPO)/resources/bin/linux/"
