package wrapper

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// toolNames lists all compiler/linker tools we create wrappers for.
var toolNames = []string{
	"cc", "c++", "gcc", "g++", "clang", "clang++",
	"ar", "ld", "ranlib", "strip",
}

// LogEntry represents a single captured tool invocation.
type LogEntry struct {
	Timestamp  string   `json:"timestamp"`
	Tool       string   `json:"tool"`
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
	WorkDir    string   `json:"work_dir"`
}

// Setup creates wrapper symlinks in a temporary directory and returns
// the wrapper directory path and log file path.
// The caller should set PATH to prepend wrapperDir, and set
// WASMIFY_LOG_FILE to logFile.
func Setup() (wrapperDir string, logFile string, cleanup func(), err error) {
	wrapperDir, err = os.MkdirTemp("", "wasmify-wrapper-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to create wrapper directory: %w", err)
	}

	logFile = filepath.Join(wrapperDir, "build.log")

	// Build the wrapper binary
	wrapperBin := filepath.Join(wrapperDir, "wasmify-wrapper")
	self, err := os.Executable()
	if err != nil {
		_ = os.RemoveAll(wrapperDir)
		return "", "", nil, fmt.Errorf("failed to get current executable: %w", err)
	}

	// Copy self as the wrapper binary (the main binary handles wrapper mode via argv[0])
	data, err := os.ReadFile(self)
	if err != nil {
		_ = os.RemoveAll(wrapperDir)
		return "", "", nil, fmt.Errorf("failed to read executable: %w", err)
	}
	if err := os.WriteFile(wrapperBin, data, 0o755); err != nil {
		_ = os.RemoveAll(wrapperDir)
		return "", "", nil, fmt.Errorf("failed to write wrapper binary: %w", err)
	}

	// Create symlinks for each tool name
	for _, name := range toolNames {
		link := filepath.Join(wrapperDir, name)
		if err := os.Symlink(wrapperBin, link); err != nil {
			_ = os.RemoveAll(wrapperDir)
			return "", "", nil, fmt.Errorf("failed to create symlink for %s: %w", name, err)
		}
	}

	cleanup = func() {
		_ = os.RemoveAll(wrapperDir)
	}

	return wrapperDir, logFile, cleanup, nil
}

// EnvForBuild returns environment variables to configure the build
// to use wrappers. It sets CC, CXX, AR, etc. to point to the wrappers,
// and sets WASMIFY_REAL_<TOOL> to the original tool paths.
func EnvForBuild(wrapperDir, logFile string, env []string) ([]string, error) {
	result := make([]string, 0, len(env)+len(toolNames)*2+2)

	// Copy existing env, filtering out CC/CXX/AR etc.
	overrideKeys := map[string]bool{
		"CC": true, "CXX": true, "AR": true,
		"LD": true, "RANLIB": true, "STRIP": true,
	}
	for _, e := range env {
		key := strings.SplitN(e, "=", 2)[0]
		if !overrideKeys[key] {
			result = append(result, e)
		}
	}

	// Set wrapper paths
	result = append(result,
		fmt.Sprintf("CC=%s", filepath.Join(wrapperDir, "cc")),
		fmt.Sprintf("CXX=%s", filepath.Join(wrapperDir, "c++")),
		fmt.Sprintf("AR=%s", filepath.Join(wrapperDir, "ar")),
		fmt.Sprintf("LD=%s", filepath.Join(wrapperDir, "ld")),
		fmt.Sprintf("RANLIB=%s", filepath.Join(wrapperDir, "ranlib")),
		fmt.Sprintf("STRIP=%s", filepath.Join(wrapperDir, "strip")),
		fmt.Sprintf("WASMIFY_LOG_FILE=%s", logFile),
	)

	// Resolve real tool paths against the PASSED env's PATH (not the process
	// PATH), so a caller can steer resolution by prepending e.g. wasi-sdk/bin —
	// the captured Makefile invokes the compiler by bare name (CC=clang) and we
	// must bind WASMIFY_REAL_clang to the wasi-sdk clang, not whatever clang the
	// host happens to expose.
	searchPath := ""
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			searchPath = strings.TrimPrefix(e, "PATH=")
		}
	}
	for _, name := range toolNames {
		realPath := lookPathIn(name, searchPath)
		if realPath == "" {
			continue // Tool not found, skip
		}
		envKey := fmt.Sprintf("WASMIFY_REAL_%s", normalizeEnvKey(name))
		result = append(result, fmt.Sprintf("%s=%s", envKey, realPath))
	}

	return result, nil
}

// lookPathIn finds an executable named `name` in the colon-separated `path`,
// falling back to the process PATH (exec.LookPath) when `path` is empty. Unlike
// exec.LookPath it honours the explicit search path the caller supplies.
func lookPathIn(name, path string) string {
	if path == "" {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
		return ""
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	return ""
}

// RunAsWrapper is called when the binary is invoked via a symlink (as a wrapper).
// It logs the invocation and execs the real tool.
func RunAsWrapper() error {
	toolName := filepath.Base(os.Args[0])

	logFile := os.Getenv("WASMIFY_LOG_FILE")
	if logFile == "" {
		return fmt.Errorf("WASMIFY_LOG_FILE not set")
	}

	// Find the real tool
	envKey := fmt.Sprintf("WASMIFY_REAL_%s", normalizeEnvKey(toolName))
	realPath := os.Getenv(envKey)
	if realPath == "" {
		return fmt.Errorf("real tool path not found for %s (env: %s)", toolName, envKey)
	}

	// Log the invocation
	wd, _ := os.Getwd()
	entry := LogEntry{
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		Tool:       toolName,
		Executable: realPath,
		Args:       os.Args[1:],
		WorkDir:    wd,
	}

	_ = appendLogEntry(logFile, entry)

	// Exec the real tool
	cmd := exec.Command(realPath, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// appendLogEntry appends one JSON-encoded LogEntry, followed by a newline, to
// logFile. The record and its newline are written with a SINGLE write() to a
// file opened O_APPEND, which is atomic on Linux even when many wrapped
// compilers (separate processes under `make --jobs N`) append concurrently — so
// records never interleave. Writing the record and the newline as two separate
// writes is the bug this guards against: the pair is not atomic, so a race
// concatenates two JSON objects onto one line, ParseLog drops the malformed
// line, and build.json silently loses a compile step (breaking wasm-build's
// archive on a clean tree). Best-effort: a marshal or open error is dropped, as
// the original logging was.
func appendLogEntry(logFile string, entry LogEntry) error {
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func normalizeEnvKey(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "+", "PLUS"))
}

// IsWrapperInvocation returns true if the current process was invoked
// as a wrapper (i.e., argv[0] matches one of the tool names).
func IsWrapperInvocation() bool {
	name := filepath.Base(os.Args[0])
	for _, t := range toolNames {
		if name == t {
			return true
		}
	}
	return false
}
