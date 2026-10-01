.DEFAULT_GOAL := help

GO ?= go
# Use the first GOPATH entry even when GOBIN is configured separately.
BIN_DIR ?= $(shell $(GO) env GOPATH | cut -d: -f1)/bin
HORIZON_HOME ?= $(HOME)/.horizon
# Select repository plugins to install.
PLUGINS ?= decision telegram
PLUGIN_NAMES := $(notdir $(patsubst %/main.go,%,$(wildcard plugins/*/main.go)))
PLUGIN_TARGETS := $(addprefix install-,$(PLUGIN_NAMES))

.PHONY: help install install-all install-plugins $(PLUGIN_TARGETS)

help:
	@printf '%s\n' \
	  'make install                 Install Horizon into GOPATH/bin' \
	  'make install-plugins         Install selected plugins into HORIZON_HOME/plugins' \
	  'make install-decision        Install the decision plugin' \
	  'make install-telegram         Install the Telegram integration plugin' \
	  'make install-all             Install Horizon and selected plugins' \
	  'Overrides: GO, BIN_DIR, HORIZON_HOME, PLUGINS'

install:
	GOBIN="$(BIN_DIR)" $(GO) install .

install-all: install install-plugins

install-plugins: $(addprefix install-,$(PLUGINS))

# Build beside the destination and replace only after a successful build.
$(PLUGIN_TARGETS): install-%:
	@set -eu; \
	  umask 077; \
	  mkdir -p "$(HORIZON_HOME)/plugins"; \
	  temporary=$$(mktemp "$(HORIZON_HOME)/plugins/.horizon-$*.XXXXXX"); \
	  trap 'rm -f "$$temporary"' EXIT HUP INT TERM; \
	  $(GO) build -o "$$temporary" "./plugins/$*"; \
	  chmod 700 "$$temporary"; \
	  mv -f "$$temporary" "$(HORIZON_HOME)/plugins/horizon-$*"; \
	  printf 'Installed %s\n' "$(HORIZON_HOME)/plugins/horizon-$*"
