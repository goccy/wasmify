package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/wasmify/internal/state"
)

// globalNonInteractive disables every fmt.Scanln-style prompt in the
// CLI. When true, code paths that previously asked the user a y/N
// question must consult declarative rules in wasmify.json's `skip`
// section or fail with a clear error. Set by --non-interactive or env
// `WASMIFY_NON_INTERACTIVE=1`.
var globalNonInteractive bool

func init() {
	if os.Getenv("WASMIFY_NON_INTERACTIVE") == "1" {
		globalNonInteractive = true
	}
}

// stripNonInteractiveFlag removes any `--non-interactive` token from
// argv, sets globalNonInteractive when found, and returns the cleaned
// slice. Called once at the top of dispatch so every subcommand sees
// argv without the global flag.
func stripNonInteractiveFlag(argv []string) []string {
	out := argv[:0:0]
	for _, a := range argv {
		if a == "--non-interactive" {
			globalNonInteractive = true
			continue
		}
		out = append(out, a)
	}
	return out
}

// shouldDeployStubHeader reports whether the given missing header is
// pre-approved for stub deployment by the project's wasmify.json
// `skip.deploy_stub_headers` list.
func shouldDeployStubHeader(s *state.State, header string) bool {
	if s == nil || s.Skip == nil {
		return false
	}
	for _, h := range s.Skip.DeployStubHeaders {
		if h == header {
			return true
		}
	}
	return false
}

// shouldSkipFile reports whether the given source file path is pre-
// approved for compile-error skip by the project's wasmify.json
// `skip.files` list. Matches either the exact path or a suffix match
// (`/foo.cc` matches `pkg/foo.cc`) so users do not need to encode the
// full bazel-out prefix.
func shouldSkipFile(s *state.State, sourcePath string) bool {
	if s == nil || s.Skip == nil {
		return false
	}
	clean := filepath.Clean(sourcePath)
	for _, f := range s.Skip.Files {
		want := filepath.Clean(f.Path)
		if clean == want {
			return true
		}
		if strings.HasSuffix(clean, string(filepath.Separator)+want) {
			return true
		}
	}
	return false
}
