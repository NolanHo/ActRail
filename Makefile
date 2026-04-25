GO ?= /usr/local/go/bin/go

.PHONY: fmt test run

fmt:
	$(GO) fmt ./...

test:
	$(GO) test ./...

run:
	$(GO) run ./cmd/actrail-server
