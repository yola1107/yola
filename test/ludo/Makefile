.PHONY: init config api build test generate all help

# init env
init:
	go install github.com/google/wire/cmd/wire@latest
	go install github.com/bufbuild/buf/cmd/buf@latest

# generate internal proto
config:
	cd .. && buf generate --template buf.gen.config.yaml

# generate api proto
api:
	buf generate --template buf.gen.yaml

# build
build:
	mkdir -p bin/
	go build -o ./bin/ludo-server ./cmd/ludo-server
	go build -o ./bin/ludo-client ./cmd/ludo-client

# test
test:
	go test ./...

# generate
generate:
	go generate ./...

# clean tracked files
clean:
	rm -rf ./bin *.exe logs ../logs *.log

# generate all
all: api config generate

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
