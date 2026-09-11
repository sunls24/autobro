GO := go
BINARY := bin/autobro
GO_FILES := $(shell find . -type f -name '*.go' -not -path './.git/*')
SOURCE_DIRS := $(sort . $(shell find cmd internal -type d))

.PHONY: build relog new

build: $(BINARY)

$(BINARY): $(GO_FILES) $(SOURCE_DIRS) go.mod go.sum Makefile
	mkdir -p "$(dir $(BINARY))"
	$(GO) build -o "$(BINARY)" ./cmd/toapi

relog: $(BINARY)
	"./$(BINARY)" -r

new: $(BINARY)
	"./$(BINARY)" $(ARGS)
