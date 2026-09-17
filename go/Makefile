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

.PHONY: all build static release test test-race fmt check-fmt vet clean install \
         verify-static e2e e2e-claude check help generate check-generate

all: build

## generate: 扫描 modules/ 重建组合根装配清单（app/modules_gen.go）
generate:
	@go run ./tools/genmodules

## check-generate: 只校验清单是否过期，不改文件（CI/测试用）
check-generate:
	@go run ./tools/genmodules -check

build: generate
	@mkdir -p $(OUT)
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(OUT)/$(BIN) ./cmd/newgate
	@echo "built $(OUT)/$(BIN)"

## static: 当前平台的全静态二进制
static: generate
	@mkdir -p $(OUT)
	$(STATIC_ENV) go build $(GOFLAGS) $(STATIC_TAGS) -ldflags '$(STATIC_LD)' \
		-o $(OUT)/$(BIN) ./cmd/newgate
	@$(MAKE) --no-print-directory verify-static

## release: 交叉编译所有平台的静态二进制到 dist/
release: generate
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

test: check-generate
	go test ./...

## test-race: 带竞态检测跑单测（并发相关改动必跑，见 docs/10-testing-security.md §6）
test-race: check-generate
	go test -race ./...

## fmt: 就地格式化
fmt:
	gofmt -l -w .

## check-fmt: 只校验格式，不改文件（CI 与 check 用）
check-fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then \
		echo "以下文件格式不对，跑 make fmt："; echo "$$out"; exit 1; \
	fi

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

# 零 token 端到端：假上游 + 假 opencode 客户端
e2e: build
	@bash ../mock/e2e.sh

## e2e-claude: 零 token 端到端（Claude Code 侧，覆盖面最广的一份）
e2e-claude: build
	@bash ../mock/e2e_claude.sh

## check: 换版本之前跑这个。全绿才有资格谈上线。
check: check-generate check-fmt vet test e2e e2e-claude
	@echo
	@echo "✓ 全绿：静态检查 + 单测 + 两条零 token 端到端"

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
	@echo "  build / install / test / test-race / fmt / vet / clean"
	@echo "  e2e / e2e-claude / check / generate / check-generate"
