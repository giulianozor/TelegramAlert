BINARY := telegram-alert
PKG := ./...
LDFLAGS :=

.PHONY: all build test lint vet clean install run fmt

all: build

build:
	go build -o $(BINARY) .

test:
	go test $(PKG)

vet:
	go vet $(PKG)

lint:
	go vet $(PKG)

fmt:
	gofmt -w .

install:
	go install .

run:
	./$(BINARY)

clean:
	rm -f $(BINARY)
