GO ?= /usr/local/go/bin/go
BUF ?= buf
PROTO_PATH := $(shell $(GO) env GOPATH)/bin:$(CURDIR)/web/node_modules/.bin:$(PATH)

.PHONY: fmt proto test run

fmt:
	$(GO) fmt ./...

proto:
	PATH="$(PROTO_PATH)" $(BUF) generate

test:
	$(GO) test ./...

run:
	$(GO) run ./cmd/actrail-server
