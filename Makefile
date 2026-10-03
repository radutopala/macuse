.PHONY: help build install uninstall restart test coverage-check _coverage-check-run lint lint-darwin app sign notarize dist clean
.DEFAULT_GOAL := help

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# The bundle version is the tag's numbers (v2026.10.1 -> 2026.10.1).
APP_VERSION ?= $(shell echo $(VERSION) | sed -E 's/^v//; s/[^0-9.].*$$//; s/^$$/0.0.0/')
LDFLAGS     := -s -w -X github.com/radutopala/mac-use/internal/buildinfo.Version=$(VERSION)
GO_IMAGE    ?= golang:1.27
LINT_IMAGE  ?= golangci/golangci-lint:v2.13.1
DIST        := dist
APP         := $(DIST)/mac-use.app
# Signing: a "Developer ID Application" identity in the keychain.
SIGN_IDENTITY ?= Developer ID Application
# Local install: the app, and a CLI link on the PATH like go install's.
INSTALL_DIR ?= /Applications
BIN_DIR     ?= $(shell go env GOPATH)/bin
INSTALLED   := $(INSTALL_DIR)/mac-use.app/Contents/MacOS/mac-use

build: ## Build bin/mac-use for this machine
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/mac-use ./cmd/mac-use

install: sign ## Install the signed app to INSTALL_DIR and link the CLI into BIN_DIR
	rm -rf $(INSTALL_DIR)/mac-use.app
	cp -R $(APP) $(INSTALL_DIR)/
	mkdir -p $(BIN_DIR)
	ln -sf $(INSTALLED) $(BIN_DIR)/mac-use

uninstall: ## Stop the service, then remove the app and the CLI link (keeps settings and approvals)
	@# [m] keeps the pattern from matching this recipe's own shell.
	@[ ! -x $(INSTALLED) ] || $(INSTALLED) service uninstall
	@pkill -TERM -f '$(INSTALL_DIR)/mac-use.app/Contents/MacOS/[m]ac-use$$' && echo "Quit the running mac-use" || true
	rm -rf $(INSTALL_DIR)/mac-use.app
	rm -f $(BIN_DIR)/mac-use

restart: install ## Install, then stop and start the server
	@# A copy opened from Finder runs without arguments; quit it so the service takes over.
	@pkill -TERM -f '$(INSTALL_DIR)/mac-use.app/Contents/MacOS/[m]ac-use$$' && echo "Quit the running mac-use" || true
	$(INSTALLED) service install

test: ## Run the tests
	go test -race -count=1 ./...

_coverage-check-run:
	go test -race -count=1 -timeout 120s -coverpkg=./... -coverprofile=coverage.out ./...
	@# Counted from the raw profile: `go tool cover -func` rounds its total to
	@# one decimal. With -coverpkg a block repeats once per test binary; it is
	@# covered if any hit it. main.go is only main(): signals, args and exit.
	@awk 'NR > 1 && $$1 !~ /cmd\/mac-use\/main\.go:/ { split($$0, f, " "); n[f[1]] = f[2]; if (f[3] > 0) hit[f[1]] = 1 } \
		END { for (b in n) { total += n[b]; if (!(b in hit) && n[b] > 0) { miss += n[b]; print "uncovered: " b " (" n[b] " stmts)" | "sort" } } \
		close("sort"); \
		if (miss > 0) { printf "Coverage is %.4f%% (%d of %d statements uncovered), required 100%%\n", 100 * (total - miss) / total, miss, total; exit 1 } \
		printf "Coverage: 100%% (%d statements)\n", total }' coverage.out

coverage-check: ## Enforce 100% coverage of the portable code (in Docker on a Mac, directly in CI)
	@if [ "$$CI" = "true" ] || [ -f /.dockerenv ]; then \
		$(MAKE) _coverage-check-run; \
	else \
		docker run --rm -v "$$(pwd)":/src -w /src $(GO_IMAGE) make _coverage-check-run; \
	fi

lint: ## Run golangci-lint for Linux and macOS builds (with auto-fix)
	docker run --rm -v "$$(pwd)":/src -w /src $(LINT_IMAGE) golangci-lint run -v --fix ./...
	$(MAKE) lint-darwin

lint-darwin:
	docker run --rm -e GOOS=darwin -e GOARCH=arm64 -v "$$(pwd)":/src -w /src $(LINT_IMAGE) golangci-lint run -v ./...

app: ## Build the universal dist/mac-use.app
	rm -rf $(APP)
	mkdir -p $(APP)/Contents/MacOS
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/mac-use-darwin-arm64 ./cmd/mac-use
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/mac-use-darwin-amd64 ./cmd/mac-use
	lipo -create -output $(APP)/Contents/MacOS/mac-use $(DIST)/mac-use-darwin-arm64 $(DIST)/mac-use-darwin-amd64
	rm $(DIST)/mac-use-darwin-arm64 $(DIST)/mac-use-darwin-amd64
	sed 's/@VERSION@/$(APP_VERSION)/g' packaging/Info.plist > $(APP)/Contents/Info.plist
	plutil -lint $(APP)/Contents/Info.plist

sign: app ## Sign the app with the hardened runtime
	codesign --force --options runtime --timestamp --sign "$(SIGN_IDENTITY)" $(APP)
	codesign --verify --strict --verbose=2 $(APP)

notarize: sign ## Notarize and staple the app (needs APPLE_ID, APPLE_APP_SPECIFIC_PASSWORD, APPLE_TEAM_ID)
	ditto -c -k --keepParent $(APP) $(DIST)/notarize.zip
	xcrun notarytool submit $(DIST)/notarize.zip --apple-id "$$APPLE_ID" --password "$$APPLE_APP_SPECIFIC_PASSWORD" --team-id "$$APPLE_TEAM_ID" --wait
	rm $(DIST)/notarize.zip
	xcrun stapler staple $(APP)
	spctl --assess --type execute --verbose $(APP)

dist: notarize ## Notarized app zip plus Linux MCP client binaries, with checksums
	cd $(DIST) && ditto -c -k --keepParent mac-use.app mac-use_$(VERSION)_macos.zip
	for arch in amd64 arm64; do \
		mkdir -p $(DIST)/linux-$$arch && cp LICENSE $(DIST)/linux-$$arch/ && \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o $(DIST)/linux-$$arch/mac-use ./cmd/mac-use && \
		tar -czf $(DIST)/mac-use_$(VERSION)_linux_$$arch.tar.gz -C $(DIST)/linux-$$arch mac-use LICENSE || exit 1; \
	done
	cd $(DIST) && shasum -a 256 mac-use_$(VERSION)_* > checksums.txt

clean: ## Remove build output
	rm -rf bin $(DIST) coverage.out
