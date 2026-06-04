# siesta — scheduled Kubernetes node-pool scaler

BINARY      := siesta
PKG         := ./cmd/siesta
BIN_DIR     := bin
FG_ZIP      := function.zip
# FunctionGraph executable name. Must match the function's Handler field
# (the Go1.x console default is "handler").
FG_BINARY   := handler

# The huaweicloud-go-runtime mirror is not in the checksum DB; treat it as private.
export GOPRIVATE := github.com/rahadiangg/*

.PHONY: all build build-fg package-fg test cover vet tidy clean

all: build

## build: local binary for the host platform
build:
	go build -o $(BIN_DIR)/$(BINARY) $(PKG)

## build-fg: Linux/amd64 executable named "handler" for FunctionGraph (Go1.x)
build-fg:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(FG_BINARY) $(PKG)

## package-fg: build-fg + zip with the handler executable at the archive root
package-fg: build-fg
	rm -f $(FG_ZIP)
	zip -q $(FG_ZIP) $(FG_BINARY)

## test: run unit tests with coverage
test:
	go test ./... -cover

## cover: write and open an HTML coverage report
cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out

## vet: static checks
vet:
	go vet ./...

## tidy: sync go.mod/go.sum
tidy:
	go mod tidy

## clean: remove build artifacts
clean:
	rm -rf $(BIN_DIR) $(FG_BINARY) $(FG_ZIP) coverage.out dist
