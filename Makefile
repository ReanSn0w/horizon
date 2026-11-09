.DEFAULT_GOAL := help

GO ?= go
HORIZON_HOME ?= $(HOME)/.horizon
BIN_DIR ?= $(HORIZON_HOME)/bin
# Select repository plugins to install.
PLUGINS ?= manager
PLUGIN_NAMES := $(filter-out agent,$(notdir $(patsubst %/main.go,%,$(wildcard plugins/*/main.go)))) telegram
PLUGIN_TARGETS := $(addprefix install-plugin-,$(PLUGIN_NAMES))

.PHONY: help install install-all install-plugins install-telegram install-browser $(PLUGIN_TARGETS)

help:
	@printf '%s\n' \
	  'make install                 Install Horizon into HORIZON_HOME/bin' \
	  'make install-plugins         Install selected plugins into HORIZON_HOME/bin' \
	  'make install-telegram        Install the Telegram integration plugin' \
	  'make install-browser         Install the Browser Use plugin' \
	  'make install-all             Install Horizon and selected plugins' \
	  'Overrides: GO, BIN_DIR, HORIZON_HOME, PLUGINS'

install:
	@set -eu; \
	  mkdir -p "$(BIN_DIR)"; \
	  temporary=$$(mktemp "$(BIN_DIR)/.horizon.XXXXXX"); \
	  trap 'rm -f "$$temporary"' EXIT HUP INT TERM; \
	  $(GO) build -o "$$temporary" .; \
	  chmod 755 "$$temporary"; \
	  mv -f "$$temporary" "$(BIN_DIR)/horizon"; \
	  printf 'Installed %s\n' "$(BIN_DIR)/horizon"

install-all: install install-plugins

install-plugins: $(addprefix install-plugin-,$(PLUGINS))

install-telegram: install-plugin-telegram
install-browser: install-plugin-browser
# Build beside the destination and replace only after a successful build.
$(PLUGIN_TARGETS): install-plugin-%:
	@set -eu; \
	  umask 077; \
	  mkdir -p "$(HORIZON_HOME)/bin"; \
	  temporary=$$(mktemp "$(HORIZON_HOME)/bin/.horizon-$*.XXXXXX"); \
	  trap 'rm -f "$$temporary"' EXIT HUP INT TERM; \
	  $(GO) build -o "$$temporary" "$(if $(filter telegram,$*),./plugins/agent,./plugins/$*)"; \
	  chmod 700 "$$temporary"; \
	  mv -f "$$temporary" "$(HORIZON_HOME)/bin/horizon-$*"; \
	  printf 'Installed %s\n' "$(HORIZON_HOME)/bin/horizon-$*"

.PHONY: build-manager
build-manager:
	$(GO) build -o horizon-manager ./plugins/manager
