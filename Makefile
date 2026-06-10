BIN := bin

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

# Render the init/unit files for the chosen paths: rewrite the binary and config
# locations baked into the packaging templates.
unit_subst = sed -e 's|/usr/bin/sermod|$(sbindir)/sermod|g' -e 's|/etc/sermo|$(SERMO_CONFDIR)|g'
# Rewrite the daemon/config paths in the sample config to the chosen dirs.
config_subst = sed -e 's|\.\./daemons|$(SERMO_DATADIR)/daemons|g' -e 's|/usr/share/sermo|$(SERMO_DATADIR)|g' -e 's|/etc/sermo|$(SERMO_CONFDIR)|g'
# Rewrite the state dir in the tmpfiles config (runtime /run/sermo is fixed).
tmpfiles_subst = sed -e 's|/var/lib/sermo|$(SERMO_STATEDIR)|g'

.PHONY: all build test vet fmt fmt-check lint check cover tidy clean \
        install install-bin install-daemons install-profiles install-config install-tmpfiles install-systemd install-openrc \
        uninstall

all: build

build:
	go build -o $(BIN)/sermoctl ./cmd/sermoctl
	go build -o $(BIN)/sermod ./cmd/sermod

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l internal cmd)"; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# Static analysis. Requires the tools on PATH (see CLAUDE.md for install
# commands): staticcheck, revive, golangci-lint (runs gosec via .golangci.yml),
# and govulncheck.
lint:
	staticcheck ./...
	revive -config revive.toml ./...
	golangci-lint run
	govulncheck ./...

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

# Full install: binaries, daemon definitions, sample config, tmpfiles.d, and both init
# systems. The persistent state directory is intentionally not created here;
# tmpfiles.d creates it with the same policy as the runtime directory.
install: install-bin install-daemons install-config install-tmpfiles install-systemd install-openrc

install-bin: build
	$(INSTALL) -Dm755 $(BIN)/sermoctl $(DESTDIR)$(bindir)/sermoctl
	$(INSTALL) -Dm755 $(BIN)/sermod $(DESTDIR)$(sbindir)/sermod

# Install every daemon definition preserving the services/apps/libs subdirectory layout.
install-daemons:
	@set -e; find daemons -type f -name '*.yml' | sed 's|^daemons/||' | while read -r f; do \
		echo "  install daemons/$$f"; \
		$(INSTALL) -Dm644 "daemons/$$f" "$(DESTDIR)$(SERMO_DATADIR)/daemons/$$f"; \
	done

# Legacy target name.
install-profiles: install-daemons

# Install the global config (kept if one already exists) and create the
# available/included service directories.
install-config:
	$(INSTALL) -d $(DESTDIR)$(SERMO_CONFDIR)/daemons-available $(DESTDIR)$(SERMO_CONFDIR)/apps-enabled
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
	rm -rf $(DESTDIR)$(SERMO_DATADIR)/daemons
	@echo "left $(DESTDIR)$(SERMO_CONFDIR) (config) in place"
	@echo "left $(DESTDIR)$(SERMO_STATEDIR) (state database) in place"
