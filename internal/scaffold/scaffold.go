package scaffold

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/wasmify/internal/wasmbuild"
)

// GenerateMakefile creates a Makefile in the output directory.
//
// When upstreamPath is under outputDir (the common case of the upstream being
// a git submodule of the output repo), the generated Makefile uses a relative
// PROJECT path so the same recipe works both locally and on CI without
// overrides.
func GenerateMakefile(outputDir, upstreamPath string) error {
	project := makefileProjectPath(outputDir, upstreamPath)
	content := fmt.Sprintf(`WASMIFY := wasmify
# Path to the upstream C/C++ project. Override on CI if your layout differs:
#   make wasm PROJECT=./path/to/upstream
PROJECT ?= %s
OUTPUT_DIR := .

.PHONY: all update wasm tools clean

# Install tools listed in arch.json (cmake, bazel, ...) plus wasi-sdk. Safe to
# re-run; already-installed tools are skipped.
tools:
	$(WASMIFY) ensure-tools $(PROJECT) --output-dir $(OUTPUT_DIR)

# Upstream changes: detect and re-run only the affected phases.
update:
	$(WASMIFY) update $(PROJECT) --output-dir $(OUTPUT_DIR)

# Build wasm binary. Depends on tools so CI runners do not need any
# pre-installed build dependencies beyond wasmify itself. --optimize
# chains a binaryen wasm-opt pass after the link.
wasm: tools
	$(WASMIFY) wasm-build --optimize --non-interactive --output-dir $(OUTPUT_DIR)

all: update wasm

clean:
	rm -rf wasm/*.wasm
`, project)

	path := filepath.Join(outputDir, "Makefile")
	// Do not overwrite if already exists
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// GenerateReleaseWorkflow creates .github/workflows/release.yml in the output directory.
func GenerateReleaseWorkflow(outputDir string) error {
	workflowDir := filepath.Join(outputDir, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		return fmt.Errorf("failed to create workflows directory: %w", err)
	}

	path := filepath.Join(workflowDir, "release.yml")
	// Do not overwrite if already exists
	if _, err := os.Stat(path); err == nil {
		return nil
	}

	content := fmt.Sprintf(`name: Build WASM
on:
  push:
    branches: [main]
    tags: ['v*']
  schedule:
    - cron: '0 0 * * 0'
  workflow_dispatch:

jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      contents: write
      id-token: write
      attestations: write
    steps:
      - uses: actions/checkout@v4
        with:
          # Upstream source is expected to be a git submodule of this repo.
          # If you vendor it differently, replace this with an explicit clone.
          submodules: recursive
      - uses: actions/setup-go@v5
        with:
          go-version: 'stable'
      - name: Install wasmify
        run: go install github.com/goccy/wasmify/cmd/wasmify@latest

      # Cache wasmify state.
      # - .wasmify/   per-project build cache (.o / .a / cache.json)
      # - ~/.config/wasmify/bin/wasi-sdk   shared toolchain (~500MB tarball)
      # The key is bumped whenever config files that affect the build change;
      # the pinned wasi-sdk version (%[1]s) is in the key so bumping the SDK
      # version busts old caches automatically.
      - name: Cache wasmify state
        uses: actions/cache@v4
        with:
          path: |
            .wasmify
            ~/.config/wasmify/bin/wasi-sdk
          key: wasmify-%[1]s-${{ runner.os }}-${{ runner.arch }}-${{ hashFiles('wasmify.json', 'build.json', 'proto/**') }}
          restore-keys: |
            wasmify-%[1]s-${{ runner.os }}-${{ runner.arch }}-

      - name: Install build tools and wasi-sdk
        # Reads arch.json and installs every tool listed under required_tools
        # plus wasi-sdk. No Coding Agent needed in CI. Skipped work when the
        # caches above restored a hit.
        run: make tools
      - name: Build WASM
        run: make wasm
      - name: Attest
        uses: actions/attest-build-provenance@v2
        with:
          subject-path: 'wasm/*.wasm'
      - name: Release
        uses: softprops/action-gh-release@v2
        if: startsWith(github.ref, 'refs/tags/')
        with:
          files: wasm/*.wasm
`, wasmbuild.WasiSDKPinnedVersion)
	return os.WriteFile(path, []byte(content), 0o644)
}

// makefileProjectPath returns the PROJECT value to embed in the Makefile.
// Prefers a relative path when the upstream lives under the output directory
// (submodule layout); otherwise returns the absolute upstream path.
func makefileProjectPath(outputDir, upstreamPath string) string {
	absOut, err := filepath.Abs(outputDir)
	if err != nil {
		return upstreamPath
	}
	absUp, err := filepath.Abs(upstreamPath)
	if err != nil {
		return upstreamPath
	}
	rel, err := filepath.Rel(absOut, absUp)
	if err != nil {
		return upstreamPath
	}
	// Only use the relative form when the upstream is actually inside the
	// output directory. A leading ".." means the project lives elsewhere on
	// disk; an absolute path is safer there.
	if rel == "." || rel == ".." || filepath.IsAbs(rel) ||
		(len(rel) >= 2 && rel[:2] == "..") {
		return absUp
	}
	return "./" + rel
}

// GenerateGitignore ensures .wasmify/ is ignored by git. Creates .gitignore
// if missing, or appends the pattern if not already present. This keeps the
// per-project data dir (wasi-sdk, build cache) out of commits.
func GenerateGitignore(outputDir string) error {
	const pattern = ".wasmify/"
	path := filepath.Join(outputDir, ".gitignore")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read .gitignore: %w", err)
	}
	text := string(existing)
	for _, line := range splitLines(text) {
		if line == pattern || line == ".wasmify" {
			return nil // already listed
		}
	}
	var out string
	if text == "" {
		out = pattern + "\n"
	} else {
		if len(text) > 0 && text[len(text)-1] != '\n' {
			text += "\n"
		}
		out = text + pattern + "\n"
	}
	return os.WriteFile(path, []byte(out), 0o644)
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// Generate creates Makefile, release workflow, and .gitignore in the output
// directory.
func Generate(outputDir, upstreamPath string) error {
	if err := GenerateMakefile(outputDir, upstreamPath); err != nil {
		return fmt.Errorf("failed to generate Makefile: %w", err)
	}
	if err := GenerateReleaseWorkflow(outputDir); err != nil {
		return fmt.Errorf("failed to generate release workflow: %w", err)
	}
	if err := GenerateGitignore(outputDir); err != nil {
		return fmt.Errorf("failed to generate .gitignore: %w", err)
	}
	return nil
}
