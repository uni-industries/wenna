# Gates. A change is ready to merge when `make ci` is green
# (docs/definition-of-done.md). Each target runs on the current host and
# knows nothing else.
#
# Every Go build goes through this file: it alone owns the build tags. The
# SQLite driver compiles full-text search (FTS5) only with `sqlite_fts5`; a
# build without it compiles and then fails at run time.

.PHONY: ci ci-backend eval build build-static

TAGS := sqlite_fts5
# cgo is required: the SQLite driver is C. A host without a C compiler fails
# the build instead of producing a binary without SQLite.
export CGO_ENABLED := 1
# Through GOFLAGS, so the wenna binaries tests build with `go build` get the
# same tags. GOFLAGS is space-separated: the tags are joined with commas.
comma := ,
empty :=
space := $(empty) $(empty)
export GOFLAGS += -tags=$(subst $(space),$(comma),$(strip $(TAGS)))

VERSION ?= dev
OUT ?= bin/wenna
LDFLAGS = -s -w -X main.version=$(VERSION)

# make build [VERSION=v1.2.3] [OUT=path]: the wenna binary for this host.
build:
	cd backend && go build -trimpath -ldflags "$(LDFLAGS)" -o "$(abspath $(OUT))" ./cmd/wenna

# A static binary that runs on any Linux distribution: build on musl (Alpine).
build-static: TAGS += sqlite_omit_load_extension osusergo netgo
build-static: LDFLAGS += -linkmode external -extldflags=-static
build-static: build

# Not a gate: real AI apps against wenna (backend/eval). Needs claude, codex
# and grok installed and signed in; calls model providers with synthetic data.
# EVAL picks the tests by name: TestFounderMonth (a few memories),
# TestFounderAtScale (1000 contacts), TestKeywordsBridgeSynonyms; all by
# default.
RUNS ?= 1
EVAL ?= TestFounder|TestKeywords
eval: TAGS += eval
eval:
	cd backend && go test -count=1 -timeout 90m -v -run '$(EVAL)' ./eval -args -runs=$(RUNS)

ci: ci-backend

ci-backend:
	@cd backend && out="$$(gofmt -l .)" && if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	cd backend && go mod tidy -diff
	cd backend && go vet ./...
	cd backend && go test -race -count=1 ./...
	# Without the tags: a build missing FTS5 must refuse to open the store.
	cd backend && GOFLAGS= go test -count=1 -run TestOpenWithoutFTS5 ./internal/sqlitestore
	cd backend && go build ./...
