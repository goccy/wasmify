# wasmify project rules

## `make lint` must pass before reporting

Before reporting any code change as complete — to the user, in a
commit message, in a PR body, or in chat — you MUST run `make lint`
from the repo root and confirm it exits cleanly. The Makefile target
shells out to `golangci-lint`; do not assume "the build succeeds"
is enough.

If lint fails, fix the issues and re-run `make lint` until it passes.
Do not announce completion of a task while lint is red. Do not push
or `git commit --amend` over a tree that has not been re-linted since
the last code edit.

Applies to every commit on this repository, including amends and
squashes.

## `make test` for any behavioural change

Run `make test` (which runs `go test -v -race ./...`) before
reporting any change that touches behavior. The e2e tests under
`testdata/simplelib_proto_wasm2go/` are driven from
`protoc-plugins/protoc-gen-wasmify-go/`'s test package; they invoke
`buf generate` as a subprocess, so `buf` and `go` must be on PATH.
