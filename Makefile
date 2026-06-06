BIN := bin

# Standard GNU-style install variables. Override on the command line, e.g.
#   make install DESTDIR=/tmp/stage PREFIX=/usr
# DESTDIR stages the install under a temporary root (for packaging); the rest
# follow the GNU directory conventions.
DESTDIR ?=
PREFIX ?= /usr/local
prefix ?= $(PREFIX)
exec_prefix ?= $(prefix)
bindir ?= $(exec_prefix)/bin
sbindir ?= $(exec_prefix)/sbin
datarootdir ?= $(prefix)/share
datadir ?= $(datarootdir)
sysconfdir ?= /etc

# Sermo-specific locations derived from the standard dirs.
SERMO_CONFDIR ?= $(sysconfdir)/sermo
SERMO_DATADIR ?= $(datadir)/sermo
SYSTEMD_UNITDIR ?= /usr/lib/systemd/system
OPENRC_INITDIR ?= $(sysconfdir)/init.d

INSTALL ?= install

# Render the init/unit files for the chosen paths: rewrite the binary and config
# locations baked into the packaging templates.
unit_subst = sed -e 's|/usr/bin/sermod|$(sbindir)/sermod|g' -e 's|/etc/sermo|$(SERMO_CONFDIR)|g'
# Rewrite the profile/config paths in the sample config to the chosen dirs.
config_subst = sed -e 's|/usr/share/sermo|$(SERMO_DATADIR)|g' -e 's|/etc/sermo|$(SERMO_CONFDIR)|g'

.PHONY: all build test vet fmt fmt-check tidy clean \
        install install-bin install-profiles install-config install-systemd install-openrc \
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

tidy:
	go mod tidy

clean:
	rm -rf $(BIN)

# Full install: binaries, profiles, sample config, and both init systems.
install: install-bin install-profiles install-config install-systemd install-openrc

install-bin: build
	$(INSTALL) -Dm755 $(BIN)/sermoctl $(DESTDIR)$(bindir)/sermoctl
	$(INSTALL) -Dm755 $(BIN)/sermod $(DESTDIR)$(sbindir)/sermod

# Install every profile preserving the services/apps/libs subdirectory layout.
install-profiles:
	@set -e; find profiles -type f -name '*.yml' | sed 's|^profiles/||' | while read -r f; do \
		echo "  install profiles/$$f"; \
		$(INSTALL) -Dm644 "profiles/$$f" "$(DESTDIR)$(SERMO_DATADIR)/profiles/$$f"; \
	done

# Install the global config (kept if one already exists) and create the
# available/enabled service directories.
install-config:
	$(INSTALL) -d $(DESTDIR)$(SERMO_CONFDIR)/apps-available $(DESTDIR)$(SERMO_CONFDIR)/apps-enabled
	@if [ -f "$(DESTDIR)$(SERMO_CONFDIR)/sermo.yml" ]; then \
		echo "  keeping existing $(DESTDIR)$(SERMO_CONFDIR)/sermo.yml"; \
	else \
		echo "  install $(SERMO_CONFDIR)/sermo.yml"; \
		$(config_subst) configs/sermo.yml > $(DESTDIR)$(SERMO_CONFDIR)/sermo.yml; \
		chmod 644 $(DESTDIR)$(SERMO_CONFDIR)/sermo.yml; \
	fi

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
	rm -rf $(DESTDIR)$(SERMO_DATADIR)/profiles
	@echo "left $(DESTDIR)$(SERMO_CONFDIR) (config) in place"
