GO ?= go
PNPM ?= pnpm
VERSION ?= $(shell cat VERSION)
REVISION ?= $(shell git rev-parse HEAD 2>/dev/null || echo dev)
IMAGE ?= tillbox:local

# 开发工具的版本只在这里定，`make deps` 按这些版本安装
GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK_VERSION := v1.9.0
GITLEAKS_VERSION := v8.30.1
LEFTHOOK_VERSION := v2.2.2
RUFF_VERSION := 0.17.1
GOBIN := $(or $(shell $(GO) env GOBIN),$(shell $(GO) env GOPATH)/bin)

# 并发敏感的包，再开 race 检测跑一遍
RACE_PKGS := ./internal/order ./internal/ledger ./internal/notify ./internal/admin ./internal/maintenance ./internal/assistance ./internal/confirm ./internal/monitor

.PHONY: build web test test-race test-pg e2e check ci release image tools deps lint fmt vuln

# 目标按写的顺序一个个跑：lint 要先装好前端依赖，几个重任务同时跑也会抢内存。make -j 不影响这里。
.NOTPARALLEL:

build: web
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -p 4 -trimpath -ldflags '-s -w -X main.version=$(VERSION) -X main.revision=$(REVISION)' -o bin/tillbox ./cmd/tillbox

web:
	$(PNPM) --dir web install --frozen-lockfile
	$(PNPM) --dir web build

test:
	$(GO) test -p 4 ./...
	$(PNPM) --dir web test
	python3 -m unittest discover -s scripts -p 'test_*.py'

test-race:
	$(GO) test -race -p 2 $(RACE_PKGS)

# 用 PostgreSQL 再跑一遍 Go 测试；TILLBOX_TEST_PG_DSN 指向测试库，make ci 会临时起一个
test-pg:
	@test -n "$$TILLBOX_TEST_PG_DSN" || { echo '需要设置 TILLBOX_TEST_PG_DSN，见 docs/development.md' >&2; exit 1; }
	$(GO) test -p 2 ./...

# 浏览器逐页检查（Playwright）：起两个演示实例打开所有页面。要先 make build，浏览器装法见 docs/development.md
e2e:
	$(PNPM) --dir web exec playwright test -c e2e

check:
	$(GO) vet -p 4 ./...
	$(PNPM) --dir web typecheck

# 本机跑一遍和 CI 相同的检查，发版前必须通过；需要 Docker
ci:
	sh scripts/ci.sh

# make release VERSION=x.y.z，步骤见 docs/release.md
release:
	sh scripts/release.sh $(VERSION)

# 前端依赖里有私有包，读包令牌取环境变量 GITHUB_TOKEN，见 docs/development.md
image:
	@test -n "$$GITHUB_TOKEN" || { echo '需要设置 GITHUB_TOKEN（能读 GitHub Packages 的令牌），见 docs/development.md' >&2; exit 1; }
	docker build --platform linux/amd64 --secret id=GITHUB_TOKEN --build-arg VERSION=$(VERSION) --build-arg REVISION=$(REVISION) -t $(IMAGE) .

# make lint 和 make vuln 用的工具，CI 也用它装。Go 工具装到 $(GOBIN)，ruff 用 uv 装，两处都要在 PATH 里。
tools:
	$(GO) install -p 4 github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO) install -p 4 golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	uv tool install --force ruff@$(RUFF_VERSION)

# 本机开发：检查工具之外再装前端依赖、浏览器检查用的 Chromium 和提交钩子
deps: tools
	$(PNPM) --dir web install --frozen-lockfile
	$(PNPM) --dir web exec playwright install chromium
	$(GO) install -p 4 github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION)
	$(GO) install -p 4 github.com/evilmartians/lefthook/v2@$(LEFTHOOK_VERSION)
	$(GOBIN)/lefthook install

# 只检查不改文件
lint:
	$(GOBIN)/golangci-lint run ./...
	$(PNPM) --dir web lint
	$(PNPM) --dir web format:check
	ruff check scripts
	ruff format --check scripts

# 按规范格式化全部代码
fmt:
	$(GOBIN)/golangci-lint fmt ./...
	$(PNPM) --dir web format
	ruff format scripts

vuln:
	$(GOBIN)/govulncheck ./...
