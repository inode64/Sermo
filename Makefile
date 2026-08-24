BIN := bin
CGO_ENABLED ?= 0
GOAMD64 ?= v1
GO_BUILD_ENV := CGO_ENABLED=$(CGO_ENABLED) GOAMD64=$(GOAMD64)
GO_PACKAGES := ./cmd/... ./internal/...

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

# Nested module for the dashboard bundler. esbuild is a tool dependency of
# this package only; sermod embeds the committed index.html and never links it.
WEB_BUILD_DIR := internal/web/build

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

.PHONY: all build build-candidate-sermoctl test vet fmt fmt-check lint modules-check actions-lint race fuzz deadcode quality-report cover-gate custom-gcl scripts-lint scripts-test semgrep yaml-fmt yaml-fmt-check yaml-lint yaml-validate markdown-check web web-check web-lint web-e2e validate check cover tidy clean \
        install install-bin install-catalog install-examples install-config install-templates install-tmpfiles install-systemd install-openrc \
        uninstall

all: build

build:
	$(GO_BUILD_ENV) go build -trimpath -buildvcs=false -ldflags '$(GO_BUILD_LDFLAGS)' -o $(BIN)/sermoctl ./cmd/sermoctl
	$(GO_BUILD_ENV) go build -trimpath -buildvcs=false -ldflags '$(GO_BUILD_LDFLAGS)' -o $(BIN)/sermod ./cmd/sermod

# The fleet updater builds this second CLI with SERMO_DATADIR pointing at its
# run-specific remote staging tree. It validates the candidate binary and
# candidate catalog together before any live path is replaced.
build-candidate-sermoctl:
	$(GO_BUILD_ENV) go build -trimpath -buildvcs=false -ldflags '$(GO_BUILD_LDFLAGS)' -o $(BIN)/sermoctl-candidate ./cmd/sermoctl

# YAML formatting and lint (yamlfmt via go install, yamllint via pip/pipx).
YAMLFMT ?= yamlfmt
YAMLLINT ?= yamllint
YAML_ROOTS = catalog examples templates docs .github
MARKDOWNLINT ?= ./node_modules/.bin/markdownlint
PLAYWRIGHT ?= ./node_modules/.bin/playwright
SHELLCHECK ?= shellcheck
RUFF ?= ruff
SEMGREP ?= semgrep
SEMGREP_TARGETS = cmd internal
ACTIONLINT ?= actionlint
FUZZ_TIME ?= 15s
# gocognit, gocyclo, dupl and perfsprint are blocking linters. Keep a focused
# cyclomatic-complexity report available for direct inspection.
QUALITY_REPORT_LINTERS = gocyclo
SCRIPT_SH = scripts/*.sh
SCRIPT_PY = scripts/*.py
PRIVATE_SCRIPT_PATHS = scripts/remote-deploy scripts/open_sermo_dashboards.py scripts/test_open_sermo_dashboards.py

yaml-fmt:
	@$(LINT_PATH) $(YAMLFMT) -conf .yamlfmt
	@python3 scripts/normalize_yaml_flow.py

yaml-fmt-check:
	@$(LINT_PATH) python3 scripts/yaml_format_check.py

yaml-lint:
	@$(LINT_PATH) $(YAMLLINT) --strict -c .yamllint.yml $(YAML_ROOTS) .semgrep .golangci.yml .custom-gcl.yml

yaml-validate: yaml-fmt-check yaml-lint

# Markdown lint for tracked docs and agent guidance (markdownlint-cli via npm).
markdown-check:
	@git ls-files -z -- '*.md' | $(LINT_PATH) xargs -0 -r $(MARKDOWNLINT) --config .markdownlint.yml

# Regenerate the embedded dashboard (internal/web/index.html) from its sources
# in internal/web/src using esbuild's Go API (in-process, no Node/npm). esbuild
# lives in the nested WEB_BUILD_DIR module so sermod/sermoctl never require it.
# Run this after editing anything under internal/web/src and commit the result.
web:
	go run -C $(WEB_BUILD_DIR) . -src ../src -out ../index.html

# Fail if the committed internal/web/index.html is out of date with its sources.
# Modeled on fmt-check; runs in CI via validate so a stale bundle can't land.
web-check:
	@tmp="$$(mktemp)"; \
	go run -C $(WEB_BUILD_DIR) . -src ../src -out "$$tmp"; \
	if ! cmp -s "$$tmp" internal/web/index.html; then \
		rm -f "$$tmp"; \
		echo "internal/web/index.html is stale; run 'make web' and commit the result"; \
		exit 1; \
	fi; \
	rm -f "$$tmp"

# Static analysis for the authored dashboard modules and browser tests. The
# vendored lit-html module and generated bundle are excluded in eslint.config.mjs.
web-lint:
	@npm run --silent lint:web
	@npm run --silent lint:css

# Browser-level dashboard flows and WCAG 2.2 AA checks. The fixture server
# serves the committed bundle and Playwright intercepts APIs with deterministic
# data, so this never starts sermod or performs service operations.
web-e2e: web-check web-lint
	@$(PLAYWRIGHT) test

# Known vulnerabilities in the npm dependency tree, the JavaScript counterpart
# of govulncheck. Dev dependencies are in scope: they run in CI and on developer
# machines, and a build-time tool is still an attack surface.
npm-audit:
	@echo "npm audit"
	@npm audit --audit-level=high

# Shell and Python helper scripts (deploy, mutation, YAML normalization).
scripts-lint:
	@echo "shellcheck $(SCRIPT_SH)"
	@$(LINT_PATH) $(SHELLCHECK) $(SCRIPT_SH)
	@echo "ruff check $(SCRIPT_PY)"
	@$(LINT_PATH) $(RUFF) check $(SCRIPT_PY)

scripts-test:
	@echo "script unit tests"
	@python3 -m unittest discover -s scripts -p 'test_*.py'

privacy-check:
	@tracked="$$(git ls-files -- $(PRIVATE_SCRIPT_PATHS))"; \
	if [ -n "$$tracked" ]; then \
		echo "operator-owned scripts must remain untracked:"; \
		echo "$$tracked"; \
		exit 1; \
	fi

# Documentation that describes the code must keep describing the code: cited
# source paths, identifiers named by skills, the linter roll-call, operator
# config keys and EN/ES parity. Each check exists because that drift reached the
# repository at least once and no other tool in the gate can see it.
docs-sync:
	@echo "docs-sync"
	@$(LINT_PATH) python3 scripts/docs_sync_check.py

# Formatting and static analysis gates; make test and make check run this first.
validate: modules-check lint actions-lint scripts-lint scripts-test privacy-check npm-audit yaml-validate markdown-check docs-sync web-e2e

# GO_TEST_FLAGS defaults to -shuffle=on so order-dependent tests surface
# locally and in CI. Override for a stable order when debugging:
#   make test GO_TEST_FLAGS=
GO_TEST_FLAGS ?= -shuffle=on

test: validate
	go test $(GO_TEST_FLAGS) $(GO_PACKAGES)
	go test -C $(WEB_BUILD_DIR) $(GO_TEST_FLAGS) .

vet:
	go vet $(GO_PACKAGES)
	go vet -C $(WEB_BUILD_DIR) .

fmt:
	gofmt -w internal cmd
	$(LINT_PATH) goimports -w internal cmd

fmt-check:
	@out="$$(gofmt -l internal cmd)"; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@out="$$( $(LINT_PATH) goimports -l internal cmd)"; \
	if [ -n "$$out" ]; then echo "goimports needed:"; echo "$$out"; exit 1; fi

# Safety packages for NilAway and cover-gate. Operation/process own start/stop/
# signal paths; locks/rules/config gate remediation policy and untrusted YAML.
SAFETY_PACKAGES := ./internal/operation ./internal/process ./internal/locks ./internal/rules ./internal/config
# golangci-lint carrying the NilAway module plugin. NilAway is not a stock
# linter, so the gate needs a bespoke binary built from .custom-gcl.yml; plain
# `golangci-lint run` fails with `plugin "nilaway" not found` against our config.
# That file only pins what to compile in — every linter, NilAway included, is
# configured in .golangci.yml and runs in the single `custom-gcl run` below.
# Both versions are pinned there, so the binary is rebuilt only when it changes.
CUSTOM_GCL := bin/custom-gcl

$(CUSTOM_GCL): .custom-gcl.yml
	@echo "golangci-lint custom"
	@$(LINT_CACHE_ENV) golangci-lint custom

custom-gcl: $(CUSTOM_GCL)

# Static analysis. Finds Go-installed tools in ~/go/bin: staticcheck,
# custom-gcl (gosec, NilAway, revive and focused bug analyzers, all configured
# in .golangci.yml), govulncheck and deadcode.
lint: fmt-check $(CUSTOM_GCL)
	@echo "go fix -diff $(GO_PACKAGES)"
	@go fix -diff $(GO_PACKAGES)
	@echo "staticcheck -checks=all $(GO_PACKAGES)"
	@$(LINT_CACHE_ENV) staticcheck -checks=all $(GO_PACKAGES)
	@echo "custom-gcl run (includes nilaway, revive)"
	@$(LINT_CACHE_ENV) $(CUSTOM_GCL) run $(GO_PACKAGES)
	@echo "govulncheck $(GO_PACKAGES)"
	@$(LINT_CACHE_ENV) govulncheck $(GO_PACKAGES)
	@echo "deadcode -test $(GO_PACKAGES)"
	@$(LINT_PATH) deadcode -test $(GO_PACKAGES)
	@echo "web-build nested module (vet, staticcheck, custom-gcl, govulncheck)"
	@go vet -C $(WEB_BUILD_DIR) .
	@go fix -C $(WEB_BUILD_DIR) -diff .
	@$(LINT_CACHE_ENV) sh -c 'cd $(WEB_BUILD_DIR) && staticcheck -checks=all . && govulncheck . && "$(CURDIR)/$(CUSTOM_GCL)" run --disable=gomodguard_v2 .'
	@$(MAKE) --no-print-directory semgrep

# Repository invariants that no generic Go linter can express: depguard bounds
# imports, these bound what may be *called* once an import is allowed. Each rule
# in .semgrep/rules/ is verified in both directions, and the --test pass makes
# that permanent: .semgrep/tests/ annotates the lines each rule must flag and
# the lines it must not, and semgrep exits non-zero when a rule stops agreeing.
# A rule that matches nothing is the same silent no-op that govet and revive
# were here before, so it has to prove itself on every run.
semgrep:
	@echo "semgrep --test .semgrep/rules"
	@out="$$($(LINT_PATH) $(SEMGREP) --test --config .semgrep/rules/ --metrics=off .semgrep/tests/ 2>&1)" \
	  || { echo "$$out"; exit 1; }
	@echo "semgrep .semgrep/rules"
	@$(LINT_PATH) $(SEMGREP) --config .semgrep/rules/ --metrics=off --error --quiet $(SEMGREP_TARGETS)

# Verify module checksums and fail when the dependency manifests are not tidy.
modules-check:
	@go mod verify
	@go mod tidy -diff
	@go -C $(WEB_BUILD_DIR) mod verify
	@go -C $(WEB_BUILD_DIR) mod tidy -diff

# Validate GitHub Actions syntax, expressions, action inputs, and unsafe shell use.
actions-lint:
	@$(LINT_PATH) $(ACTIONLINT)

# Race instrumentation is substantially slower than normal tests, so CI runs it
# in its own job instead of extending the default PR gate.
race:
	go test -race -count=1 $(GO_TEST_FLAGS) $(GO_PACKAGES)

# Keep fuzzing bounded and focused on untrusted configuration and safety
# parsers. Each Fuzz* runs for FUZZ_TIME; scheduled CI can raise FUZZ_TIME.
# GO_TEST_FLAGS is intentionally not applied: -shuffle is meaningless for -fuzz.
fuzz:
	@echo "fuzz config (LoadGlobal, LoadDocument) $(FUZZ_TIME)"
	go test -run '^$$' -fuzz '^FuzzLoadGlobal$$' -fuzztime=$(FUZZ_TIME) ./internal/config
	go test -run '^$$' -fuzz '^FuzzLoadDocument$$' -fuzztime=$(FUZZ_TIME) ./internal/config
	@echo "fuzz process (ParseSelectors, ParseStopPolicy, ParseSignal, ParseKillSignal) $(FUZZ_TIME)"
	go test -run '^$$' -fuzz '^FuzzParseSelectors$$' -fuzztime=$(FUZZ_TIME) ./internal/process
	go test -run '^$$' -fuzz '^FuzzParseStopPolicy$$' -fuzztime=$(FUZZ_TIME) ./internal/process
	go test -run '^$$' -fuzz '^FuzzParseSignal$$' -fuzztime=$(FUZZ_TIME) ./internal/process
	go test -run '^$$' -fuzz '^FuzzParseKillSignal$$' -fuzztime=$(FUZZ_TIME) ./internal/process
	@echo "fuzz rules (ParseRules) $(FUZZ_TIME)"
	go test -run '^$$' -fuzz '^FuzzParseRules$$' -fuzztime=$(FUZZ_TIME) ./internal/rules

# Advisory only (not part of lint/check): unreachable-function report from
# golang.org/x/tools/cmd/deadcode. Reflection and build tags cause false
# positives, so findings need human triage before acting on them.
deadcode:
	@$(LINT_PATH) deadcode -test $(GO_PACKAGES)

# Advisory only (not part of lint/check): keep the remaining cyclomatic
# complexity baseline visible while it is reduced in focused refactors.
# golangci-lint uses exit code 1 when it reports findings; preserve other
# non-zero codes as analyzer failures.
#
# It must use the custom binary like `lint` does: .golangci.yml declares the
# NilAway module plugin, so a stock golangci-lint exits 3 with `plugin
# "nilaway" not found` — a code this target correctly refuses to swallow.
quality-report: $(CUSTOM_GCL)
	@out="$$(mktemp)"; status=0; \
	$(LINT_CACHE_ENV) $(CUSTOM_GCL) run --enable-only=$(QUALITY_REPORT_LINTERS) --output.text.path "$$out" $(GO_PACKAGES) || status="$$?"; \
	cat "$$out"; \
	rm -f "$$out"; \
	if [ "$$status" -ne 0 ] && [ "$$status" -ne 1 ]; then exit "$$status"; fi

# Everything CI enforces: vet, formatting, static analysis, YAML gates, tests,
# and the safety-package coverage floor.
check: vet test cover-gate

# Coverage: print the total and write a browsable HTML report.
cover: validate
	go test $(GO_TEST_FLAGS) -coverprofile=coverage.out $(GO_PACKAGES)
	@go tool cover -func=coverage.out | tail -1
	@go tool cover -html=coverage.out -o coverage.html
	@echo "wrote coverage.html"

# No-regression statement-coverage floor on safety packages (see scripts/cover_gate.py).
# Skips validate so it can run after `make test` without re-running the full gate.
cover-gate:
	@echo "cover-gate $(SAFETY_PACKAGES)"
	@go test $(GO_TEST_FLAGS) -coverprofile=coverage-safety.out $(SAFETY_PACKAGES)
	@$(LINT_PATH) python3 scripts/cover_gate.py coverage-safety.out

tidy:
	go mod tidy

clean:
	rm -rf $(BIN)
	rm -f coverage.out coverage.html coverage-safety.out

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
# configured directories for services, host-specific apps, notifier fragments
# and classified watch documents — each with its `.local` sibling, the per-host
# override layer a deployment never overwrites.
install-config:
	$(call install_dirs,$(DESTDIR)$(SERMO_CONFDIR)/services $(DESTDIR)$(SERMO_CONFDIR)/apps $(DESTDIR)$(SERMO_CONFDIR)/notifiers $(DESTDIR)$(SERMO_CONFDIR)/storages $(DESTDIR)$(SERMO_CONFDIR)/networks $(DESTDIR)$(SERMO_CONFDIR)/mounts $(DESTDIR)$(SERMO_CONFDIR)/watches)
	$(call install_dirs,$(DESTDIR)$(SERMO_CONFDIR)/services.local $(DESTDIR)$(SERMO_CONFDIR)/apps.local $(DESTDIR)$(SERMO_CONFDIR)/notifiers.local $(DESTDIR)$(SERMO_CONFDIR)/storages.local $(DESTDIR)$(SERMO_CONFDIR)/networks.local $(DESTDIR)$(SERMO_CONFDIR)/mounts.local $(DESTDIR)$(SERMO_CONFDIR)/watches.local $(DESTDIR)$(SERMO_CONFDIR)/templates.local)
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
