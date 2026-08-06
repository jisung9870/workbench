GO ?= go
WB_INSTALL_DIR ?= $(HOME)/.local/bin
WB_INSTALL_PATH := $(WB_INSTALL_DIR)/wb
WB_ZSH_COMPLETION_DIR ?= $(HOME)/.local/share/zsh/site-functions
WB_ZSHRC ?= $(HOME)/.zshrc

.PHONY: build test vet install install-completion

build:
	$(GO) build -o wb ./cmd/wb

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

install:
	@mkdir -p "$(WB_INSTALL_DIR)"
	$(GO) build -trimpath -o "$(WB_INSTALL_PATH)" ./cmd/wb
	@bash scripts/install-zsh-completion.sh "$(WB_INSTALL_PATH)" "$(WB_ZSH_COMPLETION_DIR)" "$(WB_ZSHRC)"
	@printf 'installed %s\n' "$(WB_INSTALL_PATH)"

install-completion:
	@test -x "$(WB_INSTALL_PATH)" || { printf 'wb is not installed at %s\n' "$(WB_INSTALL_PATH)" >&2; exit 1; }
	@bash scripts/install-zsh-completion.sh "$(WB_INSTALL_PATH)" "$(WB_ZSH_COMPLETION_DIR)" "$(WB_ZSHRC)"
