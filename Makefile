# Every CI job (GitHub Actions) calls these targets: `make ci` runs the
# checks; CI also runs cover, snapshot and CodeQL.
BIN     := gl-issues
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
# A relative XDG_CONFIG_HOME is ignored (XDG spec), as by `gl-issues init`.
CONFIG  ?= $(if $(filter /%,$(XDG_CONFIG_HOME)),$(XDG_CONFIG_HOME),$(HOME)/.config)/gl-issues/config.toml

# Tools run at pinned versions through `go run`: nothing to install, and
# the same version locally and in CI. Override, e.g. GOLANGCI_LINT=golangci-lint.
GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
GOVULNCHECK   ?= go run golang.org/x/vuln/cmd/govulncheck@v1.8.0
GITLEAKS      ?= go run github.com/zricethezav/gitleaks/v8@v8.30.1
GORELEASER    ?= go run github.com/goreleaser/goreleaser/v2@v2.18.2
COBERTURA     ?= go run github.com/boumenot/gocover-cobertura@v1.5.0
ACTIONLINT    ?= go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12

.PHONY: all build install test race cover fmt fmt-check vet lint vuln secrets \
	ci release-check snapshot release release-tools actionlint publish-check config demo clean

all: ci

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) .

install:
	go install -trimpath -ldflags "$(LDFLAGS)" .

test:
	go test ./...

race:
	go test -race -count=1 ./...

# coverage.out (Go), coverage.xml (Cobertura)
cover:
	go test -race -count=1 -covermode=atomic -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	$(COBERTURA) < coverage.out > coverage.xml

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l $$(git ls-files '*.go')); if [ -n "$$out" ]; then echo "not gofmt'ed:"; echo "$$out"; exit 1; fi

# Also for Linux, so OS-specific helpers stay correct on both platforms.
vet:
	go vet ./...
	GOOS=linux go vet ./...

lint:
	$(GOLANGCI_LINT) run ./...

vuln:
	$(GOVULNCHECK) ./...

# Secrets in the whole git history (CI checks out full history).
secrets:
	$(GITLEAKS) git --no-banner --redact .

# What CI runs on every push and pull request.
ci: fmt-check vet lint actionlint release-check race vuln secrets build

release-check:
	$(GORELEASER) check

# Full release build without publishing: dist/ (binaries for Linux and
# macOS, archives, .deb/.rpm, checksums)
snapshot:
	$(GORELEASER) release --snapshot --clean --skip=sign,sbom

# Publish a tagged release (CI only: needs GITHUB_TOKEN,
# cosign and syft on PATH).
release:
	$(GORELEASER) release --clean

release-tools:
	go install github.com/sigstore/cosign/v3/cmd/cosign@v3.1.3
	go install github.com/anchore/syft/cmd/syft@v1.54.0

actionlint:
	$(ACTIONLINT)

# Run before every commit and push (not in CI): every commit not yet on a
# remote, and user.email, must use a GitHub noreply address; no secrets;
# no private names: one extended regexp per line in the untracked
# .git/info/private-patterns, checked against the files and every
# unpushed commit (message and diff, so a removed line counts too). No blank
# lines: one would match everything.
PRIVATE := $(shell git rev-parse --git-path info/private-patterns 2>/dev/null)
NOREPLY := @users\.noreply\.github\.com$$
publish-check: secrets
	@e=$$(git config user.email); if ! echo "$$e" | grep -qE '$(NOREPLY)'; then \
		echo "git config user.email is '$$e', not a GitHub noreply address"; exit 1; fi
	@bad=$$(git log --branches --not --remotes --format='%h author %ae%n%h committer %ce' | grep -vE '$(NOREPLY)' || true); \
		if [ -n "$$bad" ]; then echo "commits without a GitHub noreply address:"; echo "$$bad"; exit 1; fi
	@if [ ! -s "$(PRIVATE)" ]; then echo "warning: no $(PRIVATE); private names not checked"; \
	elif git grep -inE -f "$(PRIVATE)" -- . || \
		git log -p --branches --not --remotes --format='%h %an %cn %s%n%b' | grep -inE -f "$(PRIVATE)"; then \
		echo "private names found (patterns in $(PRIVATE))"; exit 1; fi
	@echo "publish-check ok"

# Create a private config file from the template (never overwrites):
# the same as `gl-issues init`.
config:
	@if [ -e "$(CONFIG)" ]; then echo "$(CONFIG) already exists"; else \
		go run . -config "$(CONFIG)" init; fi

# The README screencasts, docs/demo-dark.gif and docs/demo-light.gif: a
# made-up GitLab group served by a fake glab in a sandbox HOME, recorded
# with VHS (needs vhs, python3). demo.tape records the dark one; the light
# one swaps the VHS theme and the gl-issues theme.
DEMO_DIR ?= /tmp/gl-issues-demo
demo: build
	for t in dark light; do \
		docs/demo/setup.sh $(DEMO_DIR) ./$(BIN) $$t && \
		sed -e "s|docs/demo-dark.gif|docs/demo-$$t.gif|" \
			-e "$$( [ $$t = light ] && echo 's|Catppuccin Mocha|Catppuccin Latte|' )" \
			docs/demo/demo.tape > $(DEMO_DIR)/demo.tape && \
		GLI_DEMO=$(DEMO_DIR) BASH_SILENCE_DEPRECATION_WARNING=1 vhs $(DEMO_DIR)/demo.tape || exit 1; \
	done

clean:
	rm -rf $(BIN) dist coverage.out coverage.xml
