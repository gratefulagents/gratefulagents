.DEFAULT_GOAL := help

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Setup

.PHONY: install
install: ## Install all workspace dependencies.
	pnpm install --frozen-lockfile

##@ Development

.PHONY: dev
dev: ## Run the web dev server on :8080 (proxies API to localhost:8090).
	cd web && pnpm install && pnpm dev --port 8080

.PHONY: tauri-dev
tauri-dev: ## Build and open the packaged Tauri macOS app bundle.
	cd tauri && pnpm install && pnpm tauri build --bundles app
	open "$(CURDIR)/tauri/src-tauri/target/release/bundle/macos/gratefulagents.app"

.PHONY: tauri-web
tauri-web: ## Run the Tauri frontend in the browser (no Rust shell; set TAURI_BACKEND_URL to point the proxy at staging).
	cd tauri && pnpm install && pnpm dev

##@ Self-dev (agents: see AGENTS.md)

.PHONY: selfdev-doctor
selfdev-doctor: ## Check self-dev prerequisites (chromium, fake backend boot).
	pnpm install --frozen-lockfile
	pnpm --filter selfdev run doctor

.PHONY: selfdev-serve
selfdev-serve: ## Run the UI against the fake backend (no cluster needed; SCENARIO=default|empty|error).
	pnpm install --frozen-lockfile
	pnpm --filter selfdev run serve --scenario $(or $(SCENARIO),default)

.PHONY: selfdev-snap
selfdev-snap: ## Screenshot one route into selfdev/out (ROUTE=/... required; THEME=dark|light|both).
	@test -n "$(ROUTE)" || (echo "usage: make selfdev-snap ROUTE=/runs/demo/run-ui-polish [THEME=dark] [SCENARIO=default]" && exit 1)
	pnpm install --frozen-lockfile
	pnpm --filter selfdev run screenshot --route $(ROUTE) --theme $(or $(THEME),dark) --scenario $(or $(SCENARIO),default)

.PHONY: selfdev-snap-all
selfdev-snap-all: ## Screenshot every scenario route into selfdev/out (smoke pass; SCENARIO=default).
	pnpm install --frozen-lockfile
	pnpm --filter selfdev run snap-all --scenario $(or $(SCENARIO),default)

.PHONY: selfdev-test
selfdev-test: ## Run the selfdev harness unit tests (fake backend, fixtures, tauri-sim).
	pnpm install --frozen-lockfile
	pnpm --filter selfdev run test

##@ Build

.PHONY: web
web: ## Build the browser target into web/dist (embedded by the backend).
	pnpm install --frozen-lockfile
	pnpm --filter web build

.PHONY: tauri-build
tauri-build: ## Build the packaged Tauri macOS app bundle.
	cd tauri && pnpm install && pnpm tauri build --bundles app

.PHONY: tauri-ios-init
tauri-ios-init: ## One-time Xcode project scaffolding for Tauri iOS target.
	cd tauri && pnpm ios:init

# Override these when a newer simulator runtime is installed.
IOS_SIMULATOR ?= iPhone 17 Pro
IOS_IPAD_SIMULATOR ?= iPad Pro 13-inch (M5)

.PHONY: tauri-ios-simulator
tauri-ios-simulator: ## Run on the latest iPhone Pro simulator (override IOS_SIMULATOR to choose another device).
	cd tauri && pnpm ios:dev "$(IOS_SIMULATOR)"

.PHONY: tauri-ios-simulator-ipad
tauri-ios-simulator-ipad: ## Run on the latest 13-inch iPad Pro simulator (override IOS_IPAD_SIMULATOR to choose another device).
	cd tauri && pnpm ios:dev "$(IOS_IPAD_SIMULATOR)"

.PHONY: tauri-ios-dev
tauri-ios-dev: tauri-ios-simulator ## Alias for tauri-ios-simulator.

# Device UDID to install to (defaults to Batphone). Override: make tauri-ios-install IOS_DEVICE=<udid>
IOS_DEVICE ?= ""
IOS_IPA := tauri/src-tauri/gen/apple/build/arm64/gratefulagents.ipa

.PHONY: tauri-ios-build
tauri-ios-build: ## Build a debug-signed iOS IPA for physical devices.
	cd tauri && pnpm tauri ios build --export-method debugging

.PHONY: tauri-ios-install
tauri-ios-install: tauri-ios-build ## Build then install the IPA on the connected iPhone (unlock it first).
	xcrun devicectl device install app --device $(IOS_DEVICE) $(IOS_IPA)

.PHONY: tauri-android-init
tauri-android-init: ## One-time Android Studio project scaffolding for the Tauri Android target.
	cd tauri && pnpm tauri android init

# The debug build is signed with Android's local debug key, so it can be
# sideloaded immediately. Use a release keystore/AAB for public distribution.
ANDROID_APK := tauri/src-tauri/gen/android/app/build/outputs/apk/universal/debug/app-universal-debug.apk
ADB ?= $(if $(ANDROID_HOME),$(ANDROID_HOME)/platform-tools/adb,adb)
ANDROID_DEVICE ?=
ADB_DEVICE_ARG = $(if $(ANDROID_DEVICE),-s $(ANDROID_DEVICE),)

.PHONY: tauri-android-apk
tauri-android-apk: ## Build a debug-signed universal APK for Android sideloading.
	@test -d tauri/src-tauri/gen/android || { echo "Android project not initialized; run 'make tauri-android-init' first"; exit 1; }
	cd tauri && pnpm tauri android build --debug --apk
	@echo "APK: $(CURDIR)/$(ANDROID_APK)"

.PHONY: tauri-android-install
tauri-android-install: tauri-android-apk ## Build then install the APK on a USB-connected Android phone.
	$(ADB) $(ADB_DEVICE_ARG) install -r "$(ANDROID_APK)"

##@ Maintainer release

.PHONY: release-prereqs
release-prereqs: ## Install maintainer release dependencies for this host.
	./scripts/install-release-dependencies.sh

.PHONY: release-linux
release-linux: ## Debian: build, upload, and publish Linux/Android artifacts.
	./scripts/release.sh linux

.PHONY: release-macos
release-macos: ## Apple Silicon: build, upload, and publish macOS artifacts.
	./scripts/release.sh macos

##@ Quality

.PHONY: lint
lint: ## Lint the shared frontend source (ESLint).
	pnpm install --frozen-lockfile
	pnpm --filter @operator/frontend lint

.PHONY: test
test: ## Run the shared frontend unit tests (Vitest).
	pnpm install --frozen-lockfile
	pnpm --filter @operator/frontend test

.PHONY: typecheck
typecheck: ## Type-check the web build target (tsc -b, strictest path).
	pnpm install --frozen-lockfile
	cd web && pnpm exec tsc -b

##@ RPC contract

.PHONY: gen-rpc
gen-rpc: ## Regenerate the Go + TypeScript RPC stubs from ../rpc/*.proto (runs `make gen-rpc` at the repo root).
	$(MAKE) -C .. gen-rpc
