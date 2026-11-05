.PHONY: build test vet race cross clean

BINARY := whop
TAG := $(shell git describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null)
DIRTY := $(shell test -n "$$(git status --porcelain --untracked-files=no 2>/dev/null)" && printf '+dirty')
VERSION ?= $(if $(TAG),$(TAG)$(DIRTY),)
LDFLAGS ?= $(if $(VERSION),-X whop/internal/cli.Version=$(VERSION),)

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/whop

test:
	CGO_ENABLED=0 go test ./...

vet:
	go vet ./...

race:
	CGO_ENABLED=1 go test -race ./...

cross:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./...
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build ./...

clean:
	rm -rf bin dist
