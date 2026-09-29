BINARY  := k8s-guardian
PLUGIN  := kubectl-guard
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PREFIX  ?= /usr/local

.PHONY: build test e2e lint install uninstall clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/k8s-guardian
	ln -sf $(BINARY) bin/$(PLUGIN)

test:
	go test ./...

# Needs KUBEBUILDER_ASSETS pointing at kube-apiserver/etcd, e.g.
#   export KUBEBUILDER_ASSETS=$$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@latest use -p path)
e2e:
	go test -tags e2e -count=1 -v ./test/e2e/

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
