# AGENTS.md

`go-ctags` is a Go library that wraps `universal-ctags` for symbol extraction. It is intended for Sourcegraph use.

## Layout

- `ctags.go` — core wrapper: `Parser`/`Options`/`New` and the `ctagsProcess` that drives `universal-ctags` in interactive mode.
- `language-mapping.go` — supported language allowlist.
- `args.go` — **generated**. Do not edit by hand; it is produced from the `ctagsdotd/*.ctags` files by `gen.sh`.
- `ctagsdotd/` — `.ctags` flag definitions, one file per language.
- `testdata/` — golden snapshots used by tests.

## Setup

Requires Go (see `go.mod` for the version) and `universal-ctags` on `PATH`. For the exact ctags version used in CI, see `.github/workflows/ci.yml`.

## Build, test, generate

```bash
# Run tests
go test ./...

# Update golden snapshots
go test ./... -update

# Remove stale snapshots
go test ./... -update -clean

# Regenerate args.go after editing any ctagsdotd/*.ctags file
./gen.sh
```

## Conventions

- After changing ctags flags, edit the appropriate file in `ctagsdotd/` and re-run `./gen.sh`. CI runs `./gen.sh` followed by `git diff --exit-code`, so generated output must be committed.
- Prefer adding language support to `scip-ctags` in the Sourcegraph monorepo over this repo when a high-quality Tree-sitter grammar exists.
