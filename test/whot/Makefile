.PHONY: init config api build test generate proto all help

# init env
init:
	go install github.com/google/wire/cmd/wire@latest
	go install github.com/bufbuild/buf/cmd/buf@latest

# generate internal proto
config:
	buf generate --template buf.gen.config.yaml

# generate api proto
api:
	buf generate --template buf.gen.yaml

# build
build:
	mkdir -p bin/
	go build -o ./bin/whot-server ./cmd/whot-server
	go build -o ./bin/whot-client ./cmd/whot-client

# test
test:
	go test ./...

# generate
generate:
	go generate ./...

# generate all proto
proto: api config

# generate all
all: proto generate

# show help
help:
	@echo ''
	@echo 'Usage:'
	@echo ' make [target]'
	@echo ''
	@echo 'Targets:'
	@awk '/^[a-zA-Z_0-9-]+:/ { \
	helpMessage = match(lastLine, /^# (.*)/); \
	if (helpMessage) { \
		helpCommand = substr($$1, 0, index($$1, ":")); \
		helpMessage = substr(lastLine, RSTART + 2, RLENGTH); \
		printf "\033[36m%-22s\033[0m %s\n", helpCommand, helpMessage; \
	} \
	} \
	{ lastLine = $$0 }' $(MAKEFILE_LIST)

.DEFAULT_GOAL := help
