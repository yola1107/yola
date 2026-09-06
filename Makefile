export BUF_BREAKING_AGAINST

.PHONY: init api build generate lint check breaking race clean all help

# install development tools
init:
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

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

# run optional local lint checks without gating check or CI
lint:
	golangci-lint run ./...
	cd test && golangci-lint run --config ../.golangci.yml ./...

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
