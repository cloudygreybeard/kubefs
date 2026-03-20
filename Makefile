# kubefs Makefile

BINARY := kubefs
PREFIX ?= /usr/local
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X github.com/cloudygreybeard/kubefs/cmd.Version=$(VERSION) \
	-X github.com/cloudygreybeard/kubefs/cmd.Commit=$(COMMIT) \
	-X github.com/cloudygreybeard/kubefs/cmd.Date=$(DATE)

.PHONY: all build test lint clean install snapshot demo demo-build demo-run help

## all: Build the binary (default target)
all: build

## build: Build the binary
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

## test: Run tests
test:
	go test -v -race ./...

## lint: Run linter
lint:
	golangci-lint run

## clean: Remove build artifacts
clean:
	rm -f $(BINARY)
	rm -rf dist/

## install: Install to PREFIX/bin
install: build
	install -d $(DESTDIR)$(PREFIX)/bin
	install -m 755 $(BINARY) $(DESTDIR)$(PREFIX)/bin/$(BINARY)

## snapshot: Build a snapshot release (no publish)
snapshot:
	goreleaser release --snapshot --clean

CONTAINER_RUNTIME := $(shell command -v podman 2>/dev/null || command -v docker 2>/dev/null)
DEMO_NETWORK := kubefs-demo-net

## demo: Build images and record the demo animation (interactive, requires asciinema)
demo: demo-build
	./hack/record-demo.sh

## demo-build: Build the demo container images only
demo-build:
	$(CONTAINER_RUNTIME) build -t mock-kubeapi -f hack/demo/mock-kubeapi/Containerfile hack/demo/mock-kubeapi/
	$(CONTAINER_RUNTIME) build -t kubefs-demo -f hack/demo/Containerfile .

## demo-run: Build and run the demo interactively (no recording)
demo-run: demo-build
	-$(CONTAINER_RUNTIME) rm -f mock-kubeapi-demo 2>/dev/null
	-for cid in $$($(CONTAINER_RUNTIME) ps -aq --filter network=$(DEMO_NETWORK) 2>/dev/null); do $(CONTAINER_RUNTIME) rm -f $$cid 2>/dev/null; done
	-$(CONTAINER_RUNTIME) network rm $(DEMO_NETWORK) 2>/dev/null
	$(CONTAINER_RUNTIME) network create $(DEMO_NETWORK)
	$(CONTAINER_RUNTIME) run -d --name mock-kubeapi-demo --network $(DEMO_NETWORK) --network-alias mock-kubeapi mock-kubeapi
	sleep 1
	-$(CONTAINER_RUNTIME) run --rm -it --privileged --network $(DEMO_NETWORK) kubefs-demo
	-$(CONTAINER_RUNTIME) rm -f mock-kubeapi-demo 2>/dev/null
	-for cid in $$($(CONTAINER_RUNTIME) ps -aq --filter network=$(DEMO_NETWORK) 2>/dev/null); do $(CONTAINER_RUNTIME) rm -f $$cid 2>/dev/null; done
	-$(CONTAINER_RUNTIME) network rm $(DEMO_NETWORK) 2>/dev/null

## deps: Download dependencies
deps:
	go mod download
	go mod tidy

## help: Show this help
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':'
