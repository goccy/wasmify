// Package tools provides a registry and installer for build tools required by
// C/C++ projects on macOS and Debian/Ubuntu.
//
// A Tool records how to detect whether it is already present and how to
// install it on each supported OS. Project-specific tools can embed their own
// install specs in arch.json so that ensure-tools can install them on CI
// without additional configuration.
package tools

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// OS identifies a supported operating system target for tool installation.
type OS string

const (
	OSDarwin OS = "darwin"
	OSDebian OS = "debian" // debian-family Linux (ubuntu, debian, mint, ...)
)

// InstallSpec describes how to install a tool on a specific OS.
type InstallSpec struct {
	// Commands to execute in order. Each command is run via `sh -c`.
	// Use this for apt/brew/curl based installation.
	Commands []string `json:"commands,omitempty"`
}

// Tool describes a build tool required by a project.
type Tool struct {
	// Name is used for logging and as the default detection binary.
	Name string `json:"name"`

	// DetectCmd, if non-empty, is executed to decide whether the tool is
	// already installed (success = present). When empty, Name is looked up on
	// PATH via `command -v`.
	DetectCmd string `json:"detect_cmd,omitempty"`

	// Version is informational only — shown in logs, not enforced.
	Version string `json:"version,omitempty"`

	// Install maps an OS to the install recipe.
	Install map[OS]InstallSpec `json:"install,omitempty"`
}

// CurrentOS returns the OS identifier for the running process, or the empty
// string when the platform is not supported.
func CurrentOS() OS {
	switch runtime.GOOS {
	case "darwin":
		return OSDarwin
	case "linux":
		if isDebianFamily() {
			return OSDebian
		}
	}
	return ""
}

func isDebianFamily() bool {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "ID_LIKE=") || strings.HasPrefix(line, "ID=") {
			v := strings.ToLower(strings.Trim(strings.SplitN(line, "=", 2)[1], `"`))
			if strings.Contains(v, "debian") || strings.Contains(v, "ubuntu") {
				return true
			}
		}
	}
	return false
}

// Detect reports whether the tool is already present on the current system.
func Detect(t Tool) bool {
	if t.DetectCmd != "" {
		cmd := exec.Command("sh", "-c", t.DetectCmd)
		return cmd.Run() == nil
	}
	_, err := exec.LookPath(t.Name)
	return err == nil
}

// Install runs the install recipe for the current OS. Returns an error when
// the OS is unsupported or when no recipe is provided for it.
func Install(t Tool) error {
	osID := CurrentOS()
	if osID == "" {
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
	spec, ok := t.Install[osID]
	if !ok {
		return fmt.Errorf("no install recipe for %s on %s", t.Name, osID)
	}
	for _, c := range spec.Commands {
		fmt.Fprintf(os.Stderr, "[ensure-tools] [%s] $ %s\n", t.Name, c)
		cmd := exec.Command("sh", "-c", c)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s install failed: %w", t.Name, err)
		}
	}
	return nil
}

// Ensure installs the tool if it is not already present.
func Ensure(t Tool) error {
	if Detect(t) {
		fmt.Fprintf(os.Stderr, "[ensure-tools] %s: already installed\n", t.Name)
		return nil
	}
	fmt.Fprintf(os.Stderr, "[ensure-tools] %s: installing...\n", t.Name)
	return Install(t)
}

// sudoPrefix returns "sudo " when the current user is not root and sudo is on
// PATH, otherwise the empty string. Used so apt commands work both in CI
// (root) and on developer machines (non-root).
func sudoPrefix() string {
	if os.Geteuid() == 0 {
		return ""
	}
	if _, err := exec.LookPath("sudo"); err == nil {
		return "sudo -n "
	}
	return ""
}
