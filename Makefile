BIN := bin
CGO_ENABLED ?= 0
GOAMD64 ?= v1
GO_BUILD_ENV := CGO_ENABLED=$(CGO_ENABLED) GOAMD64=$(GOAMD64)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# Go linker flags for -ldflags. Named GO_LDFLAGS (not LDFLAGS) so Gentoo and
# other distro build environments can export LDFLAGS=-Wl,... without breaking
# go build. GO_BUILD_LDFLAGS appends Sermo's required metadata so overriding
# GO_LDFLAGS cannot detach the binary from SERMO_DATADIR's catalog.
GO_LDFLAGS ?= -s -w
GO_BUILD_LDFLAGS = $(GO_LDFLAGS) -X sermo/internal/buildinfo.Version=$(VERSION) -X sermo/internal/config.defaultCatalogDir=$(SERMO_DATADIR)/catalog

# Standard GNU-style install variables. Override on the command line, e.g.
#   make install DESTDIR=/tmp/stage PREFIX=/usr
# DESTDIR stages the install under a temporary root (for packaging); the rest
# follow the GNU directory conventions.
DESTDIR ?=
PREFIX ?= /usr
prefix ?= $(PREFIX)
exec_prefix ?= $(prefix)
bindir ?= $(exec_prefix)/bin
# On merged-/usr systems (for example Gentoo hosts with /usr/sbin -> /usr/bin),
# staging a package with a real usr/sbin directory can replace that symlink when
# the archive is extracted. If the live sbin path is a symlink to bindir, collapse
# the default install path for sermod to bindir. Packagers can still override
# sbindir explicitly when they need a distinct sbin directory.
default_sbindir = $(shell sbin='$(exec_prefix)/sbin'; bin='$(bindir)'; if [ -L "$$sbin" ] && [ "$$(readlink -f "$$sbin" 2>/dev/null)" = "$$(readlink -f "$$bin" 2>/dev/null)" ]; then printf '%s' "$$bin"; else printf '%s' "$$sbin"; fi)
sbindir ?= $(default_sbindir)
datarootdir ?= $(prefix)/share
datadir ?= $(datarootdir)
sysconfdir ?= /etc
localstatedir ?= /var

# Sermo-specific locations derived from the standard dirs.
SERMO_CONFDIR ?= $(sysconfdir)/sermo
SERMO_DATADIR ?= $(datadir)/sermo
SERMO_EXAMPLEDIR ?= $(SERMO_DATADIR)/examples
SERMO_RUNDIR ?= /run/sermo
SERMO_STATEDIR ?= $(localstatedir)/lib/sermo
SYSTEMD_UNITDIR ?= /usr/lib/systemd/system
TMPFILESDIR ?= /usr/lib/tmpfiles.d
OPENRC_INITDIR ?= $(sysconfdir)/init.d

INSTALL ?= install
install_dirs = @set -e; for d in $(1); do \
	if [ ! -d "$$d" ]; then \
		$(INSTALL) -d -m 755 "$$d"; \
	fi; \
done

# Developer tools: Go binaries in ~/go/bin; pip/pipx user scripts in ~/.local/bin.
LINT_PATH = PATH="$(HOME)/go/bin:$(HOME)/.local/bin:$(PATH)"
# staticcheck/golangci-lint write analyzer caches. Keep the default outside
# ~/.cache for restricted shells, but scope it to the checkout path so agent
# worktrees do not reuse stale absolute paths after a worktree is removed.
LINT_CACHE_DIR ?= /tmp/sermo-lint-cache-$(shell pwd | sed 's#[^A-Za-z0-9_.-]#_#g')
LINT_CACHE_ENV = $(LINT_PATH) XDG_CACHE_HOME="$${XDG_CACHE_HOME:-$(LINT_CACHE_DIR)}" GOCACHE="$${GOCACHE:-$(LINT_CACHE_DIR)/go-build}"

# Render the init/unit files for the chosen paths: rewrite the binary and config
# locations baked into the packaging templates.
unit_subst = sed -e 's|/usr/bin/sermod|$(sbindir)/sermod|g' -e 's|/etc/sermo|$(SERMO_CONFDIR)|g'
# Rewrite config paths in the sample config to the chosen dirs.
config_subst = sed -e 's|/usr/share/sermo|$(SERMO_DATADIR)|g' -e 's|/etc/sermo|$(SERMO_CONFDIR)|g' -e 's|/run/sermo|$(SERMO_RUNDIR)|g' -e 's|/var/lib/sermo|$(SERMO_STATEDIR)|g'
# Rewrite runtime/state dirs in the tmpfiles config.
tmpfiles_subst = sed -e 's|/run/sermo|$(SERMO_RUNDIR)|g' -e 's|/var/lib/sermo|$(SERMO_STATEDIR)|g'

.PHONY: all build test vet fmt fmt-check lint yaml-fmt yaml-fmt-check yaml-lint yaml-validate web web-check validate check cover tidy clean \
        install install-bin install-catalog install-examples install-config install-templates install-tmpfiles install-systemd install-openrc \
        uninstall

all: build

build:
	$(GO_BUILD_ENV) go build -trimpath -buildvcs=false -ldflags '$(GO_BUILD_LDFLAGS)' -o $(BIN)/sermoctl ./cmd/sermoctl
	$(GO_BUILD_ENV) go build -trimpath -buildvcs=false -ldflags '$(GO_BUILD_LDFLAGS)' -o $(BIN)/sermod ./cmd/sermod

# YAML formatting and lint (yamlfmt via go install, yamllint via pip/pipx).
YAMLFMT ?= yamlfmt
YAMLLINT ?= yamllint
YAML_ROOTS = catalog examples templates docs .github

yaml-fmt:
	@$(LINT_PATH) $(YAMLFMT) -conf .yamlfmt
	@python3 scripts/normalize_yaml_flow.py

yaml-fmt-check:
	@$(LINT_PATH) python3 scripts/yaml_format_check.py

yaml-lint:
	@$(LINT_PATH) $(YAMLLINT) --strict -c .yamllint.yml $(YAML_ROOTS) .golangci.yml

yaml-validate: yaml-fmt-check yaml-lint

# Regenerate the embedded dashboard (internal/web/index.html) from its sources
# in internal/web/src using esbuild's Go API (in-process, no Node/npm). Run this
# after editing anything under internal/web/src and commit the result.
web:
	go run ./internal/web/build

# Fail if the committed internal/web/index.html is out of date with its sources.
# Modeled on fmt-check; runs in CI via validate so a stale bundle can't land.
web-check:
	@tmp="$$(mktemp)"; \
	go run ./internal/web/build -out "$$tmp"; \
	if ! cmp -s "$$tmp" internal/web/index.html; then \
		rm -f "$$tmp"; \
		echo "internal/web/index.html is stale; run 'make web' and commit the result"; \
		exit 1; \
	fi; \
	rm -f "$$tmp"

# Formatting and static analysis gates; make test and make check run this first.
validate: lint yaml-validate web-check

test: validate
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l internal cmd)"; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# Static analysis. Finds Go-installed tools in ~/go/bin: staticcheck, revive,
# golangci-lint (runs gosec plus focused bug analyzers via .golangci.yml), and
# govulncheck.
lint: fmt-check
	@echo "staticcheck ./..."
	@$(LINT_CACHE_ENV) staticcheck ./...
	@echo "revive -config revive.toml ./..."
	@$(LINT_PATH) revive -config revive.toml ./...
	@echo "golangci-lint run"
	@$(LINT_CACHE_ENV) golangci-lint run
	@echo "govulncheck ./..."
	@$(LINT_CACHE_ENV) govulncheck ./...

# Everything CI enforces: vet, formatting, static analysis, YAML gates, and tests.
# test depends on validate (Go lint + yaml-validate), so those gates always run first.
check: vet test

# Coverage: print the total and write a browsable HTML report.
cover: validate
	go test -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1
	@go tool cover -html=coverage.out -o coverage.html
	@echo "wrote coverage.html"

tidy:
	go mod tidy

clean:
	rm -rf $(BIN)
	rm -f coverage.out coverage.html

# Full install: binaries, the catalog, examples, sample config, tmpfiles.d, and
# both init systems. The persistent state directory is intentionally not created
# here; tmpfiles.d creates it with the same policy as the runtime directory.
install: install-bin install-catalog install-examples install-config install-templates install-tmpfiles install-systemd install-openrc

install-bin: build
	$(INSTALL) -Dm755 $(BIN)/sermoctl $(DESTDIR)$(bindir)/sermoctl
	$(INSTALL) -Dm755 $(BIN)/sermod $(DESTDIR)$(sbindir)/sermod

# Install the whole catalog preserving the services/apps/libs/patterns layout.
install-catalog:
	@set -e; find catalog -type f -name '*.yml' | sed 's|^catalog/||' | while read -r f; do \
		echo "  install catalog/$$f"; \
		$(INSTALL) -Dm644 "catalog/$$f" "$(DESTDIR)$(SERMO_DATADIR)/catalog/$$f"; \
	done

# Install optional examples operators can copy or adapt.
install-examples:
	@set -e; find examples -type f -name '*.yml' | sed 's|^examples/||' | while read -r f; do \
		echo "  install examples/$$f"; \
		$(INSTALL) -Dm644 "examples/$$f" "$(DESTDIR)$(SERMO_EXAMPLEDIR)/$$f"; \
	done

# Install the global config (kept if one already exists) and create the
# configured directories for services, host-specific apps, storage documents,
# notifier fragments and watch documents.
install-config:
	$(call install_dirs,$(DESTDIR)$(SERMO_CONFDIR)/services $(DESTDIR)$(SERMO_CONFDIR)/apps $(DESTDIR)$(SERMO_CONFDIR)/notifiers $(DESTDIR)$(SERMO_CONFDIR)/storages $(DESTDIR)$(SERMO_CONFDIR)/networks $(DESTDIR)$(SERMO_CONFDIR)/watches)
	@if [ -f "$(DESTDIR)$(SERMO_CONFDIR)/sermo.yml" ]; then \
		echo "  keeping existing $(DESTDIR)$(SERMO_CONFDIR)/sermo.yml"; \
	else \
		echo "  install $(SERMO_CONFDIR)/sermo.yml"; \
		$(config_subst) examples/sermo.yml > $(DESTDIR)$(SERMO_CONFDIR)/sermo.yml; \
		chmod 644 $(DESTDIR)$(SERMO_CONFDIR)/sermo.yml; \
	fi

install-templates:
	@if [ -f "$(DESTDIR)$(SERMO_CONFDIR)/templates/default-alert.yml" ]; then \
		echo "  keeping existing $(DESTDIR)$(SERMO_CONFDIR)/templates/default-alert.yml"; \
	else \
		echo "  install $(SERMO_CONFDIR)/templates/default-alert.yml"; \
		$(INSTALL) -Dm644 templates/default-alert.yml "$(DESTDIR)$(SERMO_CONFDIR)/templates/default-alert.yml"; \
	fi

# systemd-tmpfiles config that creates /run/sermo and the state dir at 0700.
# Apply on a live system with: systemd-tmpfiles --create sermo.conf
install-tmpfiles:
	$(call install_dirs,$(DESTDIR)$(TMPFILESDIR))
	$(tmpfiles_subst) packaging/systemd/sermo.conf > $(DESTDIR)$(TMPFILESDIR)/sermo.conf
	chmod 644 $(DESTDIR)$(TMPFILESDIR)/sermo.conf

install-systemd:
	$(call install_dirs,$(DESTDIR)$(SYSTEMD_UNITDIR))
	$(unit_subst) packaging/systemd/sermod.service > $(DESTDIR)$(SYSTEMD_UNITDIR)/sermod.service
	chmod 644 $(DESTDIR)$(SYSTEMD_UNITDIR)/sermod.service

install-openrc:
	$(call install_dirs,$(DESTDIR)$(OPENRC_INITDIR))
	$(unit_subst) packaging/openrc/sermod > $(DESTDIR)$(OPENRC_INITDIR)/sermod
	chmod 755 $(DESTDIR)$(OPENRC_INITDIR)/sermod

uninstall:
	rm -f $(DESTDIR)$(bindir)/sermoctl $(DESTDIR)$(sbindir)/sermod
	rm -f $(DESTDIR)$(SYSTEMD_UNITDIR)/sermod.service $(DESTDIR)$(OPENRC_INITDIR)/sermod
	rm -f $(DESTDIR)$(TMPFILESDIR)/sermo.conf
	rm -rf $(DESTDIR)$(SERMO_DATADIR)/catalog
	rm -rf $(DESTDIR)$(SERMO_EXAMPLEDIR)
	@echo "left $(DESTDIR)$(SERMO_CONFDIR) (config) in place"
	@echo "left $(DESTDIR)$(SERMO_STATEDIR) (state database) in place"
