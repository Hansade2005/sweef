# Developer entry points. CI runs the same commands (see .github/workflows/ci.yml).
GO ?= go
export CGO_ENABLED = 0

.PHONY: all build vet fmt fmt-check embeds size test test-all check bench-gui run clean help

all: check

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed 's/:.*## /\t/'

build: ## build ./dist-less binary at ./sweef
	$(GO) build -o sweef ./cmd/sweef

vet: ## go vet (composite literal check off, as in CI)
	$(GO) vet -composites=false ./...

fmt: ## format our code (third_party is left alone)
	gofmt -w $$(git ls-files '*.go' | grep -v '^third_party/')

fmt-check: ## fail if our code is not gofmt-clean
	@out=$$(gofmt -l $$(git ls-files '*.go' | grep -v '^third_party/')); \
	  if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi

embeds: ## rebuild the compressed embeds (.js.z, web.zar, gofont.zar, zoneinfo.zar) after editing JS, UI assets or fonts
	$(GO) generate ./internal/engine ./internal/ui ./internal/font ./internal/intl

size: ## release-flags linux/amd64 build: size, headroom to the budget, and where the bytes are (per package and section)
	SWEEF_SIZE_TABLE=1 DIST=$${TMPDIR:-/tmp}/sweef-size scripts/release/build.sh --targets linux/amd64 --skip-launchers --skip-cdm-host

test: ## short tests (what CI runs on Linux)
	$(GO) test -short -count=1 ./...

test-all: ## including the slow, FATE-gated tests
	$(GO) test -count=1 -timeout 60m ./...

check: fmt-check vet test ## everything a PR must pass

bench-gui: ## in-process GUI latency report (writes internal/app/BENCH.md)
	$(GO) run ./internal/app/cmd/appbench

run: build ## open the desktop browser
	./sweef gui

clean:
	rm -f sweef appbench *.test *.prof coverage.out
	rm -rf dist
