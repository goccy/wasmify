.PHONY: tidy
tidy:
	go mod tidy
	go mod tidy -modfile=tools/go.mod

.PHONY: lint
lint:
	@CGO_ENABLED=0 go tool -modfile=tools/go.mod golangci-lint run

.PHONY: test
test:
	@go test -v -race ./...
