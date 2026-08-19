BIN := bin/metaprompt

.PHONY: build install test check fmt vet dry-run clean

build: $(BIN)

$(BIN): $(shell find . -name '*.go' -o -name '*.mustache')
	go build -o $(BIN) ./cmd/metaprompt

# Put metaprompt on $PATH (GOBIN, else $(go env GOPATH)/bin).
install:
	go install ./cmd/metaprompt

test:
	go test ./...

# Everything CI would run.
check: fmt vet test

fmt:
	gofmt -l -w .

vet:
	go vet ./...

# The request the tool would send, without spending a token.
dry-run: build
	$(BIN) -n testdata/example.mustache

clean:
	rm -rf bin
