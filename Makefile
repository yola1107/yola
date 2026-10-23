export BUF_BREAKING_AGAINST

BUF ?= buf
GOLANGCI_LINT ?= golangci-lint
GOLANGCI_LINT_VERSION := v2.15.0

.DEFAULT_GOAL := help
.PHONY: init api build generate fmt lint-config lint lint-fix lint-version check breaking race clean all help

init: ## 安装开发工具
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

api: ## 生成根 module 的 API Protobuf 代码
	$(BUF) generate --template buf.gen.yaml

build: ## 编译根 module 的全部包
	go build ./...

generate: ## 生成代码并整理根 module 的依赖
	go generate ./...
	go mod tidy

lint-version: ## 校验 golangci-lint 版本
	@$(GOLANGCI_LINT) version --short | grep -Fx -e $(GOLANGCI_LINT_VERSION) -e $(patsubst v%,%,$(GOLANGCI_LINT_VERSION)) >/dev/null || { \
		echo "golangci-lint 版本不符或未安装：需要 $(GOLANGCI_LINT_VERSION)，请运行 make init" >&2; exit 1; \
	}

fmt: lint-version ## 格式化两个 module 的手写 Go 代码
	$(GOLANGCI_LINT) fmt ./...
	cd test && $(GOLANGCI_LINT) fmt --config ../.golangci.yml ./...

lint-config: lint-version ## 校验 lint 配置
	$(GOLANGCI_LINT) config verify --config .golangci.yml

lint: lint-version ## 检查两个 module，全部执行后返回整体结果
	@lint_status=0; \
	for module_dir in . test; do \
		printf 'Lint: %s\n' "$$module_dir"; \
		(cd "$$module_dir" && $(GOLANGCI_LINT) run --config "$(CURDIR)/.golangci.yml" ./...) || lint_status=1; \
	done; \
	exit $$lint_status

lint-fix: fmt ## 格式化并自动修复两个 module 的 lint 问题
	$(GOLANGCI_LINT) run --fix ./...
	cd test && $(GOLANGCI_LINT) run --fix --config ../.golangci.yml ./...

check: ## 检查协议、依赖，并对两个 module 运行静态分析和测试
	$(BUF) lint
	go mod tidy -diff
	go vet ./...
	staticcheck ./...
	go test ./...
	cd test && go mod tidy -diff
	cd test && go vet ./...
	cd test && staticcheck ./...
	cd test && go test ./...
	git diff --check
	git diff --cached --check

breaking: ## 按 BUF_BREAKING_AGAINST 指定的基线检查 API 兼容性
	@test -n "$${BUF_BREAKING_AGAINST}" || (echo "BUF_BREAKING_AGAINST is required (for example: .git#ref=<tag-or-commit>)" >&2; exit 2)
	$(BUF) breaking --against "$${BUF_BREAKING_AGAINST}"

race: ## 对两个 module 运行竞态检测测试
	go test -race ./...
	cd test && go test -race ./...

clean: ## 删除本地构建产物
	rm -rf -- ./bin ./*.exe

all: ## 依次生成 API、生成其他代码并运行检查
	$(MAKE) api
	$(MAKE) generate
	$(MAKE) check

help: ## 显示帮助
	@printf '用法：make <目标>\n\n'
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
