// Package bazelcapture captures Bazel compilation commands by running a real
// bazel build (so generated files exist) and then querying bazel's action
// graph with `bazel aquery`. This replaces the compiler-wrapper approach for
// Bazel projects, whose hermetic C++ toolchains ignore CC/CXX env vars and
// therefore bypass the wrappers.
package bazelcapture

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/goccy/wasmify/internal/wrapper"
)

// Capture runs `bazel build <target>` in workDir to produce generated files
// and artifacts, then runs `bazel aquery 'deps(<target>)'` to obtain the full
// list of actions that were (or would be) executed. Each CppCompile,
// CppArchive, and CppLink action is converted to a wrapper.LogEntry so the
// downstream generate-build / wasm-build pipeline can process it unchanged.
//
// If the bazel build itself fails, its stderr is already streamed to the
// caller's stderr; the error returned here just carries the non-zero exit so
// the user can fix the underlying build problem. Aquery is not run when the
// build failed.
//
// extraBazelArgs are passed to both `bazel build` and `bazel aquery` (for
// flags like `-c opt` that affect action configuration).
//
// When outDir is non-empty and <outDir>/.bazelrc exists, the file is passed
// to bazel via the startup-time --bazelrc flag. This lets the wrapper repo
// override toolchain selection (e.g. force preinstalled_make_toolchain)
// without touching the upstream submodule's bazel config.
// Capture builds the given bazel labels and returns the merged action
// graph as compile/archive/link log entries. Multiple targets are
// passed in a single bazel build invocation and a single aquery
// expression (`deps(t1) union deps(t2) ...`) so the resulting log is
// the union — which is exactly what downstream parse-headers /
// wasm-build want when several libraries fold into one wasm artefact.
func Capture(workDir, outDir string, targets []string, extraBazelArgs []string) ([]wrapper.LogEntry, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("bazelcapture: at least one target is required")
	}
	rcPath := findBazelrc(outDir)
	if err := runBazelBuild(workDir, rcPath, targets, extraBazelArgs); err != nil {
		return nil, err
	}
	execRoot, err := bazelInfo(workDir, rcPath, "execution_root")
	if err != nil {
		return nil, err
	}
	actions, err := runAquery(workDir, rcPath, targets, extraBazelArgs)
	if err != nil {
		return nil, err
	}
	return toLogEntries(actions, execRoot), nil
}

func findBazelrc(outDir string) string {
	if outDir == "" {
		return ""
	}
	p := filepath.Join(outDir, ".bazelrc")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// bazelArgs prepends --bazelrc (startup flag, must appear before the verb)
// to the given command args.
func bazelArgs(rcPath string, tail ...string) []string {
	if rcPath == "" {
		return tail
	}
	return append([]string{"--bazelrc=" + rcPath}, tail...)
}

func runBazelBuild(workDir, rcPath string, targets []string, extra []string) error {
	tail := []string{"build"}
	tail = append(tail, extra...)
	tail = append(tail, targets...)
	args := bazelArgs(rcPath, tail...)
	cmd := exec.Command("bazel", args...)
	cmd.Dir = workDir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	fmt.Fprintf(os.Stderr, "[bazelcapture] $ bazel %s\n", strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bazel build failed: %w", err)
	}
	return nil
}

func bazelInfo(workDir, rcPath, key string) (string, error) {
	args := bazelArgs(rcPath, "info", key)
	cmd := exec.Command("bazel", args...)
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("bazel info %s failed: %w", key, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// aqueryAction mirrors the jsonproto shape we care about. Bazel fields are
// camelCase in jsonproto output.
type aqueryAction struct {
	Mnemonic        string   `json:"mnemonic"`
	Arguments       []string `json:"arguments"`
	ConfigurationID int      `json:"configurationId"`
}

// aqueryConfig tells us whether a configuration belongs to the host/exec
// side (compilers, codegen tools) or the actual target platform. Host-side
// actions should never be compiled for wasm — they only exist to produce
// inputs for the target-side build.
type aqueryConfig struct {
	ID       int    `json:"id"`
	Mnemonic string `json:"mnemonic"`
	IsTool   bool   `json:"isTool"`
}

type aqueryOutput struct {
	Actions        []aqueryAction `json:"actions"`
	Configurations []aqueryConfig `json:"configuration"`
}

func runAquery(workDir, rcPath string, targets []string, extra []string) ([]aqueryAction, error) {
	// `deps(t1) union deps(t2) union ...` returns the dependency
	// closure for every selected target in a single invocation.
	parts := make([]string, len(targets))
	for i, t := range targets {
		parts[i] = fmt.Sprintf("deps(%s)", t)
	}
	expr := strings.Join(parts, " union ")
	tail := []string{"aquery"}
	tail = append(tail, extra...)
	tail = append(tail, "--output=jsonproto", expr)
	args := bazelArgs(rcPath, tail...)
	cmd := exec.Command("bazel", args...)
	cmd.Dir = workDir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	fmt.Fprintf(os.Stderr, "[bazelcapture] $ bazel %s\n", strings.Join(args, " "))
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bazel aquery failed: %w\n%s", err, stderr.String())
	}
	var parsed aqueryOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse aquery output: %w", err)
	}
	return filterTargetActions(parsed), nil
}

// filterTargetActions drops actions that run under a host/exec
// configuration (protoc, codegen tools, etc.) and Bazel's header
// self-check actions so only target-platform compile/archive/link steps
// end up in build.log. Host-side actions produce binaries that run during
// the build, not code linked into the target; header-check actions use
// -fsyntax-only and write no real object (output ends with ".processed").
// Replaying either for wasm would be incorrect and massively inflates the
// step count (on googlesql the header checks alone are ~300 extra steps).
func filterTargetActions(out aqueryOutput) []aqueryAction {
	tool := make(map[int]bool, len(out.Configurations))
	for _, c := range out.Configurations {
		tool[c.ID] = c.IsTool
	}
	result := make([]aqueryAction, 0, len(out.Actions))
	for _, a := range out.Actions {
		if tool[a.ConfigurationID] {
			continue
		}
		if isHeaderCheckAction(a) {
			continue
		}
		result = append(result, a)
	}
	return result
}

// isHeaderCheckAction recognises Bazel's `process_headers` CppCompile
// actions — syntax-only checks that produce no object file. They appear
// under the same CppCompile mnemonic as real compiles, so we fingerprint
// them by the `-fsyntax-only` flag or the .processed output marker.
func isHeaderCheckAction(a aqueryAction) bool {
	for _, arg := range a.Arguments {
		if arg == "-fsyntax-only" {
			return true
		}
		if strings.HasSuffix(arg, ".processed") {
			return true
		}
	}
	return false
}

// toLogEntries converts aquery actions to the LogEntry format written by the
// compiler wrapper. Only C++ compile, archive, and link actions are kept —
// the rest (FileWrite, TemplateExpand, CppModuleMap, ...) are not relevant
// to downstream build.json normalization.
func toLogEntries(actions []aqueryAction, execRoot string) []wrapper.LogEntry {
	const ts = "1970-01-01T00:00:00Z"
	var entries []wrapper.LogEntry
	for _, a := range actions {
		if len(a.Arguments) == 0 {
			continue
		}
		tool := toolFromAction(a)
		if tool == "" {
			continue
		}
		entries = append(entries, wrapper.LogEntry{
			Timestamp:  ts,
			Tool:       tool,
			Executable: a.Arguments[0],
			Args:       a.Arguments[1:],
			WorkDir:    execRoot,
		})
	}
	return entries
}

// toolFromAction decides the "tool" field for LogEntry based on the bazel
// action mnemonic. Downstream normalization in internal/buildjson keys off
// the tool name to classify compile vs archive vs link steps.
func toolFromAction(a aqueryAction) string {
	switch a.Mnemonic {
	case "CppCompile", "ObjcCompile":
		// Even though arguments[0] is often cc_wrapper.sh on Bazel, the tool
		// label is recorded as "clang++" (or "clang" for .c) so the existing
		// wrapper log parser treats it like a C++ compile step. This matches
		// the shape of the historical build.log.
		if containsSource(a.Arguments, ".c") && !containsSource(a.Arguments, ".cc") &&
			!containsSource(a.Arguments, ".cpp") && !containsSource(a.Arguments, ".cxx") {
			return "clang"
		}
		return "clang++"
	case "CppArchive":
		return "ar"
	case "CppLink":
		return "clang++"
	}
	return ""
}

// containsSource reports whether any argument ends with suffix. Used to
// distinguish .c vs .cc compiles. filepath.Ext would also work but keeps an
// allocation we do not need.
func containsSource(args []string, suffix string) bool {
	for _, s := range args {
		if filepath.Ext(s) == suffix {
			return true
		}
	}
	return false
}
