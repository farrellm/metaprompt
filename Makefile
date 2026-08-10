BIN := bin/metaprompt

.PHONY: build test check fmt vet clean

build: $(BIN)

$(BIN): $(shell find . -name '*.go' -o -name '*.mustache')
	go build -o $(BIN) ./cmd/metaprompt

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
