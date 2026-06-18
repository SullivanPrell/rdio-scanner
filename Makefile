################################################################################
## Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
##
## This program is free software: you can redistribute it and/or modify
## it under the terms of the GNU General Public License as published by
## the Free Software Foundation, either version 3 of the License, or
## (at your option) any later version.
##
## This program is distributed in the hope that it will be useful,
## but WITHOUT ANY WARRANTY; without even the implied warranty of
## MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
## GNU General Public License for more details.
##
## You should have received a copy of the GNU General Public License
## along with this program.  If not, see <http://www.gnu.org/licenses/>
################################################################################

app := rdio-scanner
date := 2026/05/16
ver := 7.0.0-dev

# Detect local platform for run target
LOCAL_OS   := $(shell go env GOOS   2>/dev/null || uname -s | tr '[:upper:]' '[:lower:]')
LOCAL_ARCH := $(shell go env GOARCH 2>/dev/null || uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
LOCAL_BIN  := $(if $(filter windows,$(LOCAL_OS)),$(app).exe,$(app))

client := $(wildcard client-nuxt/*.json client-nuxt/*.ts client-nuxt/app/**/*.vue client-nuxt/app/**/*.ts)
server := $(wildcard server/*.go)

build  = @cd server && GOOS=$(1) GOARCH=$(3) go build -o ../dist/$(2)-$(3)/$(4)
pandoc = @test -d dist/$(1)-$(2) || mkdir -p dist/$(1)-$(2) && pandoc -f markdown -o dist/$(1)-$(2)/$(3) --resource-path docs:docs/platforms $(4) docs/webapp.md docs/faq.md CHANGELOG.md
zip    = @cd dist/$(1)-$(2) && zip -q ../$(app)-$(1)-$(2)-v$(ver).zip * && cd ..

.PHONY: all help clean run webapp container dist sed
.PHONY: sdrangel sdrangel-source sdrangel-snap
.PHONY: freebsd freebsd-amd64
.PHONY: linux linux-386 linux-amd64 linux-arm linux-arm64
.PHONY: macos macos-amd64 macos-arm64
.PHONY: windows windows-amd64

help: ## Show available targets
	@echo ""
	@echo "  Rdio Scanner $(ver)"
	@echo ""
	@echo "  Development"
	@grep -E '^(run|clean)[^:]*:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "    \033[36mmake %-20s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "  Distribution"
	@grep -E '^(all|dist|container|sed)[^:]*:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "    \033[36mmake %-20s\033[0m %s\n", $$1, $$2}'
	@grep -E '^(linux|macos|windows|freebsd)[^:]*:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "    \033[36mmake %-20s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "  SDRangel install (run on the Raspberry Pi)"
	@grep -E '^sdrangel[^:]*:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "    \033[36mmake %-20s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "  Variables"
	@echo "    ver          Version string               (default: $(ver))"
	@echo "    date         Release date                 (default: $(date))"
	@echo ""

all: clean dist ## Clean then build all distribution packages

run: ## Stop any running instance (service or direct), rebuild everything, and run locally
	@sudo systemctl stop rdio-scanner 2>/dev/null || true
	@sudo systemctl disable rdio-scanner 2>/dev/null || true
	@pkill -x rdio-scanner 2>/dev/null || true
	@pkill -x sdrangelsrv 2>/dev/null || true
	@pkill -x trunk-recorder 2>/dev/null || true
	@sleep 1
	@rm -fr server/webapp client-nuxt/.nuxt client-nuxt/.output node_modules/.cache/nuxt $(LOCAL_BIN) server/$(LOCAL_BIN)
	@echo "Building client..."
	@cd client-nuxt && test -d node_modules || yarn install
	@cd client-nuxt && yarn build
	@echo "Building $(app) for $(LOCAL_OS)/$(LOCAL_ARCH)..."
	@cd server && GOOS=$(LOCAL_OS) GOARCH=$(LOCAL_ARCH) go build -o ../$(LOCAL_BIN)
	@echo "Preparing headless audio sink (SDRangel)..."
	@bash scripts/sdr-audio-prep.sh || true
	@echo "Starting $(app)..."
	@./$(LOCAL_BIN)

clean: ## Remove build artefacts (node_modules, dist, server/webapp, local binary)
	@rm -fr client-nuxt/node_modules client-nuxt/.nuxt client-nuxt/.output dist server/webapp $(LOCAL_BIN) server/$(LOCAL_BIN)

# ── SDRangel install (two interchangeable paths; run ON the Pi) ───────────────
# The provisioning REST flow needs an sdrangelsrv whose FFTW planner is
# thread-safe, or creating a device set races the planner and crashes its API.

sdrangel: sdrangel-source ## Install sdrangelsrv (default: thread-safe source build)

sdrangel-source: ## Build & install a thread-safe sdrangelsrv from source (Pi5/arm64, ~30 min)
	sudo bash scripts/sdrangel-source.sh

sdrangel-snap: ## Install sdrangelsrv from the prebuilt snap (NOTE: snap is amd64-only, not the arm64 Pi)
	@bash scripts/sdrangel-snap.sh

container: webapp linux-amd64 ## Build and push multi-arch container image (requires Podman)
	@podman login docker.io
	@podman manifest rm localhost/rdio-scanner:latest || true
	@podman build --platform linux/amd64,linux/arm,linux/arm64 --pull --manifest rdio-scanner:latest .
	@podman manifest push --format v2s2 localhost/rdio-scanner:latest docker://docker.io/chuot/rdio-scanner:latest

dist: freebsd linux macos windows ## Build all distribution zip packages

sed: ## Stamp version and date into source files (usage: make sed ver=X.Y.Z date=YYYY/MM/DD)
	@sed -i -re "s|^(\s*\"version\":).*$$|\1 \"$(ver)\"|" client-nuxt/package.json
	@sed -i -re "s|^(const\s+Version\s+=).*$$|\1 \"$(ver)\"|" server/version.go
	@sed -i -re "s|v[0-9]+\.[0-9]+\.[0-9]+|v$(ver)|" COMPILING.md README.md docs/docker/README.md docs/platforms/*.md
	@sed -i -re "s|[0-9]{4}/[0-9]{2}/[0-9]{2}|$(date)|" docs/docker/README.md docs/platforms/*.md

webapp: server/webapp/index.html ## Build the Nuxt client into server/webapp/

server/webapp/index.html: $(client)
	@cd client-nuxt && test -d node_modules || yarn install
	@cd client-nuxt && yarn build

freebsd: freebsd-amd64 ## Build FreeBSD packages
freebsd-amd64: webapp dist/$(app)-freebsd-amd64-v$(ver).zip

dist/$(app)-freebsd-amd64-v$(ver).zip: $(server)
	$(call pandoc,freebsd,amd64,rdio-scanner.pdf,docs/platforms/freebsd.md)
	$(call build,freebsd,freebsd,amd64,$(app))
	$(call zip,freebsd,amd64,$(app))

linux: linux-386 linux-amd64 linux-arm linux-arm64 ## Build Linux packages (386, amd64, arm, arm64)
linux-386: webapp dist/$(app)-linux-386-v$(ver).zip
linux-amd64: webapp dist/$(app)-linux-amd64-v$(ver).zip
linux-arm: webapp dist/$(app)-linux-arm-v$(ver).zip
linux-arm64: webapp dist/$(app)-linux-arm64-v$(ver).zip

dist/$(app)-linux-386-v$(ver).zip: $(server)
	$(call pandoc,linux,386,rdio-scanner.pdf,docs/platforms/linux.md)
	$(call build,linux,linux,386,$(app))
	$(call zip,linux,386,$(app))

dist/$(app)-linux-amd64-v$(ver).zip: $(server)
	$(call pandoc,linux,amd64,rdio-scanner.pdf,docs/platforms/linux.md)
	$(call build,linux,linux,amd64,$(app))
	$(call zip,linux,amd64,$(app))

dist/$(app)-linux-arm-v$(ver).zip: $(server)
	$(call pandoc,linux,arm,rdio-scanner.pdf,docs/platforms/linux.md)
	$(call build,linux,linux,arm,$(app))
	$(call zip,linux,arm,$(app))

dist/$(app)-linux-arm64-v$(ver).zip: $(server)
	$(call pandoc,linux,arm64,rdio-scanner.pdf,docs/platforms/linux.md)
	$(call build,linux,linux,arm64,$(app))
	$(call zip,linux,arm64,$(app))

macos: macos-amd64 macos-arm64 ## Build macOS packages (amd64, arm64)
macos-amd64: webapp dist/$(app)-macos-amd64-v$(ver).zip
macos-arm64: webapp dist/$(app)-macos-arm64-v$(ver).zip

dist/$(app)-macos-amd64-v$(ver).zip: $(server)
	$(call pandoc,macos,amd64,rdio-scanner.pdf,docs/platforms/macos.md)
	$(call build,darwin,macos,amd64,$(app))
	$(call zip,macos,amd64,$(app))

dist/$(app)-macos-arm64-v$(ver).zip: $(server)
	$(call pandoc,macos,arm64,rdio-scanner.pdf,docs/platforms/macos.md)
	$(call build,darwin,macos,arm64,$(app))
	$(call zip,macos,arm64,$(app))

windows: windows-amd64 ## Build Windows packages (amd64)
windows-amd64: webapp dist/$(app)-windows-amd64-v$(ver).zip

dist/$(app)-windows-amd64-v$(ver).zip: $(server)
	$(call pandoc,windows,amd64,rdio-scanner.pdf,docs/platforms/windows.md)
	$(call build,windows,windows,amd64,$(app).exe)
	$(call zip,windows,amd64,$(app))
