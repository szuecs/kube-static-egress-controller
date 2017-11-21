default: build

# test targets
.PHONY: test

test:
	go test -v -race $(shell go list ./... | grep -v /vendor/)

# The build targets allow to build the binary and docker image
.PHONY: build build.docker

BINARY        ?= kube-static-egress-controller
SOURCES        = $(shell find . -name '*.go')
IMAGE         ?= registry.opensource.zalan.do/teapot/$(BINARY)
VERSION       ?= $(shell git describe --tags --always --dirty)
BUILD_FLAGS   ?= -v
LDFLAGS       ?= -X main.version=$(VERSION) -X main.buildstamp=$(shell date -u '+%Y-%m-%d_%I:%M:%S%p') -X main.githash=$(shell git rev-parse HEAD) -w -s

build: build/$(BINARY)

build/$(BINARY): $(SOURCES)
	CGO_ENABLED=0 go build -o build/$(BINARY) $(BUILD_FLAGS) -ldflags "$(LDFLAGS)" .

build.push: build.docker
	docker push "$(IMAGE):$(VERSION)"

build.docker:
	docker build --rm --tag "$(IMAGE):$(VERSION)" .

clean:
	@rm -rf build
