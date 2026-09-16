BIN      := newgate
OUT      := $(CURDIR)/bin
DIST     := $(CURDIR)/dist
PREFIX   ?= $(HOME)/.local
GITREV   := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILDTS  := $(shell date -u '+%Y-%m-%d_%H:%M:%SZ')
COMMITTS := $(shell git log -1 --date=format:'%Y-%m-%d_%H:%M' --format=%cd 2>/dev/null || echo unknown)
VERSION  := $(GITREV)
GOFLAGS  := -trimpath
LDFLAGS  := -s -w -X main.version=$(GITREV) -X main.buildTime=$(BUILDTS) -X main.commitTime=$(COMMITTS)

STATIC_ENV   := CGO_ENABLED=0
STATIC_TAGS  := -tags netgo,osusergo
STATIC_LD    := $(LDFLAGS) -extldflags '-static'

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: all build static release test fmt vet clean install verify-static e2e help

all: build

build:
	@mkdir -p $(OUT)
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(OUT)/$(BIN) ./cmd/newgate
	@echo "built $(OUT)/$(BIN)"

## static: 当前平台的全静态二进制
static:
	@mkdir -p $(OUT)
	$(STATIC_ENV) go build $(GOFLAGS) $(STATIC_TAGS) -ldflags '$(STATIC_LD)' \
		-o $(OUT)/$(BIN) ./cmd/newgate
	@$(MAKE) --no-print-directory verify-static

## release: 交叉编译所有平台的静态二进制到 dist/
release:
	@mkdir -p $(DIST)
	@fails=""; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		out=$(DIST)/$(BIN)-$$os-$$arch; \
		if $(STATIC_ENV) GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) $(STATIC_TAGS) \
			-ldflags '$(STATIC_LD)' -o $$out ./cmd/newgate 2>/dev/null; then \
			echo "  ok  $$os/$$arch"; \
		else \
			echo "  --  $$os/$$arch  (skipped: local Go toolchain cannot target it)"; \
			fails="$$fails $$os/$$arch"; rm -f $$out; \
		fi; \
	done; \
	[ -z "$$fails" ] || echo "not built:$$fails"
	@cd $(DIST) && sha256sum $(BIN)-* > SHA256SUMS 2>/dev/null || shasum -a 256 $(BIN)-* > SHA256SUMS
	@ls -lh $(DIST)

verify-static:
	@echo "── $(OUT)/$(BIN) ──"
	@ls -lh $(OUT)/$(BIN) | awk '{print "  大小:", $$5}'
	@if command -v file >/dev/null; then file -b $(OUT)/$(BIN) | sed 's/^/  file: /'; fi
	@if command -v ldd >/dev/null; then \
		if ldd $(OUT)/$(BIN) 2>&1 | grep -q 'not a dynamic executable\|statically linked'; then \
			echo "  ✓ 全静态（无动态依赖）"; \
		else \
			echo "  ✗ 仍有动态依赖:"; ldd $(OUT)/$(BIN) | sed 's/^/    /'; exit 1; \
		fi; \
	fi
	@$(OUT)/$(BIN) version | sed 's/^/  /'

test:
	go test ./...

fmt:
	gofmt -l -w .

vet:
	go vet ./...

install: build
	@mkdir -p $(PREFIX)/bin
	install -m 0755 $(OUT)/$(BIN) $(PREFIX)/bin/$(BIN)
	@echo "installed to $(PREFIX)/bin/$(BIN)"

## install-static: 装静态版（推荐实测用，跨机器拷贝也能跑）
install-static: static
	@mkdir -p $(PREFIX)/bin
	install -m 0755 $(OUT)/$(BIN) $(PREFIX)/bin/$(BIN)
	@echo "installed static to $(PREFIX)/bin/$(BIN)"

clean:
	rm -rf $(OUT) $(DIST)

# 零 token 端到端：假上游 + 假 opencode
e2e: build
	@bash ../mock/e2e.sh

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
	@echo "  build / install / test / fmt / vet / clean / e2e"
