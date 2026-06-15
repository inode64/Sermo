BIN := bin
GOAMD64 ?= v1
GO_BUILD_ENV := GOAMD64=$(GOAMD64)

# Standard GNU-style install variables. Override on the command line, e.g.
#   make install DESTDIR=/tmp/stage PREFIX=/usr
# DESTDIR stages the install under a temporary root (for packaging); the rest
# follow the GNU directory conventions.
DESTDIR ?=
PREFIX ?= /usr
prefix ?= $(PREFIX)
exec_prefix ?= $(prefix)
bindir ?= $(exec_prefix)/bin
sbindir ?= $(exec_prefix)/sbin
datarootdir ?= $(prefix)/share
datadir ?= $(datarootdir)
sysconfdir ?= /etc
localstatedir ?= /var

# Sermo-specific locations derived from the standard dirs.
SERMO_CONFDIR ?= $(sysconfdir)/sermo
SERMO_DATADIR ?= $(datadir)/sermo
SERMO_STATEDIR ?= $(localstatedir)/lib/sermo
SYSTEMD_UNITDIR ?= /usr/lib/systemd/system
TMPFILESDIR ?= /usr/lib/tmpfiles.d
OPENRC_INITDIR ?= $(sysconfdir)/init.d

INSTALL ?= install

# Go-installed developer tools live in ~/go/bin on local machines.
LINT_PATH = PATH="$(HOME)/go/bin:$(PATH)"
# staticcheck/golangci-lint write analyzer caches. Keep the default outside
# ~/.cache for restricted shells, but scope it to the checkout path so agent
# worktrees do not reuse stale absolute paths after a worktree is removed.
LINT_CACHE_DIR ?= /tmp/sermo-lint-cache-$(shell pwd | sed 's#[^A-Za-z0-9_.-]#_#g')
LINT_CACHE_ENV = $(LINT_PATH) XDG_CACHE_HOME="$${XDG_CACHE_HOME:-$(LINT_CACHE_DIR)}"

# Render the init/unit files for the chosen paths: rewrite the binary and config
# locations baked into the packaging templates.
unit_subst = sed -e 's|/usr/bin/sermod|$(sbindir)/sermod|g' -e 's|/etc/sermo|$(SERMO_CONFDIR)|g'
# Rewrite the catalog/config paths in the sample config to the chosen dirs.
config_subst = sed -e 's|\.\./catalog|$(SERMO_DATADIR)/catalog|g' -e 's|/usr/share/sermo|$(SERMO_DATADIR)|g' -e 's|/etc/sermo|$(SERMO_CONFDIR)|g'
# Rewrite the state dir in the tmpfiles config (runtime /run/sermo is fixed).
tmpfiles_subst = sed -e 's|/var/lib/sermo|$(SERMO_STATEDIR)|g'

.PHONY: all build test vet fmt fmt-check lint check cover tidy clean \
        install install-bin install-catalog install-config install-tmpfiles install-systemd install-openrc \
        uninstall

all: build

build:
	$(GO_BUILD_ENV) go build -o $(BIN)/sermoctl ./cmd/sermoctl
	$(GO_BUILD_ENV) go build -o $(BIN)/sermod ./cmd/sermod

test:
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
lint:
	@echo "staticcheck ./..."
	@$(LINT_CACHE_ENV) staticcheck ./...
	@echo "revive -config revive.toml ./..."
	@$(LINT_PATH) revive -config revive.toml ./...
	@echo "golangci-lint run"
	@$(LINT_CACHE_ENV) golangci-lint run
	@echo "govulncheck ./..."
	@$(LINT_PATH) govulncheck ./...

# Everything CI enforces: formatting, vet, static analysis, and the test suite.
check: fmt-check vet lint test

# Coverage: print the total and write a browsable HTML report.
cover:
	go test -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1
	@go tool cover -html=coverage.out -o coverage.html
	@echo "wrote coverage.html"

tidy:
	go mod tidy

clean:
	rm -rf $(BIN)
	rm -f coverage.out coverage.html

# Full install: binaries, the catalog, sample config, tmpfiles.d, and both init
# systems. The persistent state directory is intentionally not created here;
# tmpfiles.d creates it with the same policy as the runtime directory.
install: install-bin install-catalog install-config install-tmpfiles install-systemd install-openrc

install-bin: build
	$(INSTALL) -Dm755 $(BIN)/sermoctl $(DESTDIR)$(bindir)/sermoctl
	$(INSTALL) -Dm755 $(BIN)/sermod $(DESTDIR)$(sbindir)/sermod

# Install the whole catalog preserving the services/apps/libs subdirectory layout.
install-catalog:
	@set -e; find catalog -type f -name '*.yml' | sed 's|^catalog/||' | while read -r f; do \
		echo "  install catalog/$$f"; \
		$(INSTALL) -Dm644 "catalog/$$f" "$(DESTDIR)$(SERMO_DATADIR)/catalog/$$f"; \
	done

# Install the global config (kept if one already exists) and create the
# available/included service directories. `apps` is kept as a legacy include
# alias for hosts that still store service files there.
install-config:
	$(INSTALL) -d $(DESTDIR)$(SERMO_CONFDIR)/catalog-available $(DESTDIR)$(SERMO_CONFDIR)/services $(DESTDIR)$(SERMO_CONFDIR)/apps
	@if [ -f "$(DESTDIR)$(SERMO_CONFDIR)/sermo.yml" ]; then \
		echo "  keeping existing $(DESTDIR)$(SERMO_CONFDIR)/sermo.yml"; \
	else \
		echo "  install $(SERMO_CONFDIR)/sermo.yml"; \
		$(config_subst) configs/sermo.yml > $(DESTDIR)$(SERMO_CONFDIR)/sermo.yml; \
		chmod 644 $(DESTDIR)$(SERMO_CONFDIR)/sermo.yml; \
	fi

# systemd-tmpfiles config that creates /run/sermo and the state dir at 0700.
# Apply on a live system with: systemd-tmpfiles --create sermo.conf
install-tmpfiles:
	$(INSTALL) -d $(DESTDIR)$(TMPFILESDIR)
	$(tmpfiles_subst) packaging/systemd/sermo.conf > $(DESTDIR)$(TMPFILESDIR)/sermo.conf
	chmod 644 $(DESTDIR)$(TMPFILESDIR)/sermo.conf

install-systemd:
	$(INSTALL) -d $(DESTDIR)$(SYSTEMD_UNITDIR)
	$(unit_subst) packaging/systemd/sermod.service > $(DESTDIR)$(SYSTEMD_UNITDIR)/sermod.service
	chmod 644 $(DESTDIR)$(SYSTEMD_UNITDIR)/sermod.service

install-openrc:
	$(INSTALL) -d $(DESTDIR)$(OPENRC_INITDIR)
	$(unit_subst) packaging/openrc/sermod > $(DESTDIR)$(OPENRC_INITDIR)/sermod
	chmod 755 $(DESTDIR)$(OPENRC_INITDIR)/sermod

uninstall:
	rm -f $(DESTDIR)$(bindir)/sermoctl $(DESTDIR)$(sbindir)/sermod
	rm -f $(DESTDIR)$(SYSTEMD_UNITDIR)/sermod.service $(DESTDIR)$(OPENRC_INITDIR)/sermod
	rm -f $(DESTDIR)$(TMPFILESDIR)/sermo.conf
	rm -rf $(DESTDIR)$(SERMO_DATADIR)/catalog
	@echo "left $(DESTDIR)$(SERMO_CONFDIR) (config) in place"
	@echo "left $(DESTDIR)$(SERMO_STATEDIR) (state database) in place"
