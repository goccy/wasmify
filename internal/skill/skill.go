// Package skill exposes the embedded wasmify SKILL.md and helpers to write it
// into a project's .claude/skills/ directory.
package skill

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed SKILL.md
var Content string

// InstallPath returns the directory where the skill is installed for a given
// root (either the project's output dir or the cwd).
func InstallPath(root string) string {
	return filepath.Join(root, ".claude", "skills", "wasmify")
}

// Install writes SKILL.md into <root>/.claude/skills/wasmify/. The directory
// is created if missing. When force is false and SKILL.md already exists, the
// file is preserved so hand edits are not clobbered.
func Install(root string, force bool) (string, error) {
	dir := InstallPath(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create skill directory: %w", err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if !force {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	if err := os.WriteFile(path, []byte(Content), 0o644); err != nil {
		return "", fmt.Errorf("failed to write SKILL.md: %w", err)
	}
	return path, nil
}
