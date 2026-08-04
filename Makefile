GO ?= go
WB_INSTALL_DIR ?= $(HOME)/.local/bin
WB_INSTALL_PATH := $(WB_INSTALL_DIR)/wb

.PHONY: build test vet install

build:
	$(GO) build -o wb ./cmd/wb

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

install:
	@mkdir -p "$(WB_INSTALL_DIR)"
	$(GO) build -trimpath -o "$(WB_INSTALL_PATH)" ./cmd/wb
	@printf 'installed %s\n' "$(WB_INSTALL_PATH)"
