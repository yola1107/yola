export BUF_BREAKING_AGAINST

GOLANGCI_LINT_VERSION := v2.15.0

.PHONY: init api build generate fmt lint lint-fix lint-version check breaking race clean all help

# install development tools
init:
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# generate root API protobuf
api:
	buf generate --template buf.gen.yaml

# build root module packages
build:
	go build ./...

# run root module generators and tidy dependencies
generate:
	go generate ./...
	go mod tidy

# verify the installed linter matches the repository-pinned version
lint-version:
	@golangci-lint version --short | grep -Fx -e $(GOLANGCI_LINT_VERSION) -e $(patsubst v%,%,$(GOLANGCI_LINT_VERSION)) >/dev/null || { \
		echo "golangci-lint version mismatch or missing: expected $(GOLANGCI_LINT_VERSION); run make init" >&2; exit 1; \
	}

# format both Go modules using the configured formatters; modifies source files
fmt: lint-version
	golangci-lint fmt ./...
	cd test && golangci-lint fmt --config ../.golangci.yml ./...

# run optional local lint checks without gating check or CI
lint: lint-version
	golangci-lint run ./...
	cd test && golangci-lint run --config ../.golangci.yml ./...

# format both Go modules, then apply supported lint fixes; modifies source files
lint-fix: fmt
	golangci-lint run --fix ./...
	cd test && golangci-lint run --fix --config ../.golangci.yml ./...

# run repository checks without rewriting tracked files
check:
	buf lint
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

# check API compatibility against an explicit Buf input
breaking:
	@test -n "$${BUF_BREAKING_AGAINST}" || (echo "BUF_BREAKING_AGAINST is required (for example: .git#ref=<tag-or-commit>)" >&2; exit 2)
	buf breaking --against "$${BUF_BREAKING_AGAINST}"

# run all tests with the race detector
race:
	go test -race ./...
	cd test && go test -race ./...

# remove local build artifacts
clean:
	rm -rf -- ./bin ./*.exe

# generate root module and run repository checks
all:
	$(MAKE) api
	$(MAKE) generate
	$(MAKE) check

# show help
help:
	@echo ''
	@echo 'Usage:'
	@echo ' make [target]'
	@echo ''
	@echo 'Targets:'
	@awk '/^[a-zA-Z_-]+:/ { \
		helpMessage = match(lastLine, /^# (.*)/); \
		if (helpMessage) { \
			helpCommand = substr($$1, 0, index($$1, ":")-1); \
			helpMessage = substr(lastLine, RSTART + 2, RLENGTH); \
			printf "\033[36m%-22s\033[0m %s\n", helpCommand, helpMessage; \
		} \
	} \
	{ lastLine = $$0 }' $(MAKEFILE_LIST)

.DEFAULT_GOAL := help
