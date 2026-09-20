BIN      := newgate
OUT      := $(CURDIR)/bin
DIST     := $(CURDIR)/dist
PREFIX   ?= $(HOME)/.local
GITREV   := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# 时间戳一律用**本地时区**（这台机器就是 UTC+8），并且带时区后缀。
# 2026-09-18 之前 buildTime 走 `date -u`（UTC，结尾 Z）、commitTime 走 git 的本地
# 时间，同一行里两个时区，看的人得自己换算。用户原话：「版本号时间统一改成
# UTC+8，别搞两个时间了」。
BUILDTS  := $(shell date '+%Y-%m-%d_%H:%M:%S%z')
COMMITTS := $(shell git log -1 --date=format:'%Y-%m-%d_%H:%M:%S%z' --format=%cd 2>/dev/null || echo unknown)
VERSION  := $(GITREV)
GOFLAGS  := -trimpath
LDFLAGS  := -s -w -X main.version=$(GITREV) -X main.buildTime=$(BUILDTS) -X main.commitTime=$(COMMITTS)

STATIC_ENV   := CGO_ENABLED=0
STATIC_TAGS  := -tags netgo,osusergo
STATIC_LD    := $(LDFLAGS) -extldflags '-static'

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: all build static release test test-race fmt check-fmt vet clean install \
         verify-static e2e check ci help generate check-generate

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

## vet: 静态检查。先 check-generate：清单过期时 vet 报的是 import 找不到，
## 那是个指不到真因的错（2026-09-20 之前还得先跑 make ext 准备发行版 checkout，
## 那条路已经删了——现在清单只有本仓库一个来源，构建完全离线）。
vet: check-generate
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

## e2e: 零 token 端到端（内核的**机制面**）
##
## 内容是：真二进制起来当代理 → curl 打数据面（档位解析、上游严格性）→ 控制端点
## （停机、优雅交接）→ 运行期开关与 schema-repair。**产品面**（真客户端接管、后台
## 分类器改道、窗口注入、DeepSeek 尾部形状）跟着那些模块住在发行版里，由发行版的
## mock/ 脚本锁——那边跑的是同一份内核机制，只是经过了产品的装配。
e2e: build
	@bash mock/e2e_claude.sh

## check-i18n: 账本是否过期 + 本地化静态检查（不出网、不花钱，CI 里跑的就是它）
check-i18n:
	@go run ./tools/i18n extract -check
	@go run ./tools/i18n check

## i18n-sync: 把缺的译文补齐（调本机网关跑 LLM）。**开发者本地跑**，绝不进 CI——
## 翻译要花钱、要一台在跑的网关，那不是 push 该付的代价。
i18n-sync:
	@go run ./tools/i18n sync --lang zh-Hans

## check: 换版本之前跑这个。全绿才有资格谈上线。
check: check-generate check-fmt vet check-i18n test e2e
	@echo
	@echo "✓ 全绿：静态检查 + 单测 + 零 token 端到端（机制面）"

## ci: 在本地按 GitHub Actions 的三档跑一遍，**推之前先跑这个**。
##
## 与 check 的区别有两处，都是为了「让本地等于 runner」：
##
##   1. 把 NEWGATE_HOME 指到一个空的临时目录。这是关键——CI 的 runner 是台干净
##      机器，没有 ~/.config/newgate；而开发机永远有，于是「测试偷偷依赖开发机
##      配置」这类问题在本地永远看不见。2026-09-18 那次 CI 连续三次红就是它：
##      forward 包里走 newTestServer() 的用例没有 Watch，退化成直接读盘，
##      runner 上读不到配置 → 整包 400。
##   2. 多跑一遍 -race（check 不含，runner 上是一个独立 job）。
##
## 只钉 NEWGATE_HOME 而不动 HOME：Go 的模块缓存和构建缓存在 HOME 下，改掉会让
## 这条命令退化成每次全量重编。
ci:
	@dir=$$(mktemp -d); trap 'rm -rf "$$dir"' EXIT; \
	export NEWGATE_HOME="$$dir/home"; \
	echo "── 沙箱 NEWGATE_HOME=$$NEWGATE_HOME（模拟干净 runner）"; \
	$(MAKE) --no-print-directory check-fmt && \
	$(MAKE) --no-print-directory vet && \
	$(MAKE) --no-print-directory check-generate && \
	$(MAKE) --no-print-directory check-i18n && \
	echo "── static: 单测" && go test ./... && \
	echo "── race: 竞态检测" && go test -race ./... && \
	echo "── e2e: 零 token 端到端" && \
	$(MAKE) --no-print-directory e2e && \
	echo && echo "✓ CI 三档本地全绿（static / race / e2e）"

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
	@echo "  build / install / test / test-race / fmt / vet / clean"
	@echo "  e2e / check / ci / generate / check-generate / check-i18n / i18n-sync"
