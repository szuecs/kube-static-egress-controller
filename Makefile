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
TAG           ?= $(VERSION)
DOCKERFILE     ?= Dockerfile

build: build/$(BINARY)
build.linux: build/linux/$(BINARY)
build.linux.amd64: build/linux/amd64/$(BINARY)
build.linux.arm64: build/linux/arm64/$(BINARY)

build/$(BINARY): $(SOURCES)
	CGO_ENABLED=0 go build -o build/$(BINARY) $(BUILD_FLAGS) -ldflags "$(LDFLAGS)" .

build/linux/$(BINARY): $(SOURCES)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(BUILD_FLAGS) -o build/linux/$(BINARY) -ldflags "$(LDFLAGS)" .

build/linux/amd64/$(BINARY): $(SOURCES)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(BUILD_FLAGS) -o build/linux/amd64/$(BINARY) -ldflags "$(LDFLAGS)" .

build/linux/arm64/$(BINARY): $(SOURCES)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build $(BUILD_FLAGS) -o build/linux/arm64/$(BINARY) -ldflags "$(LDFLAGS)" .

build.push: build.docker
	docker push "$(IMAGE):$(VERSION)"

build.docker: build.linux
	docker build --rm -t "$(IMAGE):$(TAG)" -f $(DOCKERFILE) --build-arg TARGETARCH= .

clean:
	@rm -rf build
