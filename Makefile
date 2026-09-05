APP=localdns
VERSION?=0.3.2

.PHONY: build test race fmt vet run check clean release

build:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w -X github.com/bithubakkount/dns/internal/app.Version=$(VERSION)" -o bin/$(APP)-darwin-arm64 ./cmd/localdns

test:
	go test ./...

check: fmt vet test

race:
	go test -race ./...

fmt:
	gofmt -w cmd internal

vet:
	go vet ./...

run:
	go run ./cmd/localdns --config configs/localdns.yaml

clean:
	rm -rf bin dist

release:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w -X github.com/bithubakkount/dns/internal/app.Version=$(VERSION)" -o dist/$(APP)-$(VERSION)-darwin-arm64 ./cmd/localdns
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w -X github.com/bithubakkount/dns/internal/app.Version=$(VERSION)" -o dist/$(APP)-$(VERSION)-darwin-amd64 ./cmd/localdns
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w -X github.com/bithubakkount/dns/internal/app.Version=$(VERSION)" -o dist/$(APP)-$(VERSION)-linux-arm64 ./cmd/localdns
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X github.com/bithubakkount/dns/internal/app.Version=$(VERSION)" -o dist/$(APP)-$(VERSION)-linux-amd64 ./cmd/localdns
	shasum -a 256 dist/*
