package binaryen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// OptimizeOptions tunes the wasm-opt invocation. The defaults are
// "size at all costs" — appropriate for the wasmify ship-it artefact
// where any speed regression at runtime is dwarfed by the wasm-load
// cost.
type OptimizeOptions struct {
	// InstallDir is the Binaryen install root (a directory whose
	// `bin/wasm-opt` exists). Empty means DetectOrInstall.
	InstallDir string

	// Verbose mirrors the CLI flag; when true, wasm-opt's stdout/
	// stderr is plumbed through so users can see the pass list.
	Verbose bool
}

// Result captures what Optimize did so callers can format reports.
type Result struct {
	InputPath  string
	OutputPath string
	BeforeSize int64
	AfterSize  int64
}

// SavedBytes returns the byte delta. Negative numbers mean the
// optimised wasm grew — Optimize itself rejects that case, so callers
// will only see positive values in practice.
func (r Result) SavedBytes() int64 { return r.BeforeSize - r.AfterSize }

// SavedFraction returns the saved fraction in [0, 1].
func (r Result) SavedFraction() float64 {
	if r.BeforeSize == 0 {
		return 0
	}
	return float64(r.SavedBytes()) / float64(r.BeforeSize)
}

// Optimize runs `wasm-opt -Oz` plus the standard strip passes on
// inputPath, writing to outputPath. When inputPath == outputPath the
// file is rewritten atomically (tmp file → rename) and only when the
// optimised binary is strictly smaller than the original — never
// regress the artefact silently.
func Optimize(inputPath, outputPath string, opts OptimizeOptions) (Result, error) {
	res := Result{InputPath: inputPath, OutputPath: outputPath}

	inInfo, err := os.Stat(inputPath)
	if err != nil {
		return res, fmt.Errorf("stat input wasm: %w", err)
	}
	res.BeforeSize = inInfo.Size()

	dir := opts.InstallDir
	if dir == "" {
		dir, err = DetectOrInstall()
		if err != nil {
			return res, fmt.Errorf("locate binaryen: %w", err)
		}
	}
	wasmOpt := WasmOptPath(dir)

	// Always write to a tmp file first so a wasm-opt failure (or a
	// size regression) leaves the original untouched.
	tmpOut := outputPath + ".tmp"
	args := []string{
		"-Oz",
		// wasi-sdk emits wasm with these post-MVP features turned
		// on (memory.copy, sign-ext, mutable globals, multivalue,
		// reference types). wasm-opt refuses to read the binary
		// otherwise, even when the passes themselves don't use the
		// features. Enabling them only changes which input wasm is
		// accepted; the optimised output keeps whatever feature set
		// the original had.
		"--enable-bulk-memory",
		"--enable-sign-ext",
		"--enable-mutable-globals",
		"--enable-multivalue",
		"--enable-reference-types",
		"--enable-nontrapping-float-to-int",
		"--strip-debug",
		"--strip-producers",
		"--strip-target-features",
		"--converge",
		"-o", tmpOut,
		inputPath,
	}
	cmd := exec.Command(wasmOpt, args...)
	if opts.Verbose {
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmpOut)
		return res, fmt.Errorf("wasm-opt failed: %w", err)
	}

	outInfo, err := os.Stat(tmpOut)
	if err != nil {
		_ = os.Remove(tmpOut)
		return res, fmt.Errorf("stat optimised wasm: %w", err)
	}
	res.AfterSize = outInfo.Size()

	// Fail closed on regression — do not silently ship a larger wasm.
	if res.AfterSize >= res.BeforeSize {
		_ = os.Remove(tmpOut)
		return res, fmt.Errorf("wasm-opt produced no size improvement (%d → %d bytes); leaving original in place",
			res.BeforeSize, res.AfterSize)
	}

	// Atomic rename so observers never see a partial output even
	// when output == input.
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		_ = os.Remove(tmpOut)
		return res, fmt.Errorf("ensure output dir: %w", err)
	}
	if err := os.Rename(tmpOut, outputPath); err != nil {
		_ = os.Remove(tmpOut)
		return res, fmt.Errorf("rename optimised wasm into place: %w", err)
	}
	return res, nil
}
