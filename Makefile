BINARY  := k8s-guardian
PLUGIN  := kubectl-guard
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PREFIX  ?= /usr/local

.PHONY: build test lint install uninstall clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/k8s-guardian
	ln -sf $(BINARY) bin/$(PLUGIN)

test:
	go test ./...

lint:
	go vet ./...
	test -z "$$(gofmt -l .)"

install: build
	install -m 0755 bin/$(BINARY) $(PREFIX)/bin/$(BINARY)
	ln -sf $(PREFIX)/bin/$(BINARY) $(PREFIX)/bin/$(PLUGIN)

uninstall:
	rm -f $(PREFIX)/bin/$(BINARY) $(PREFIX)/bin/$(PLUGIN)

clean:
	rm -rf bin dist
