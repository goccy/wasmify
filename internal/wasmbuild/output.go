package wasmbuild

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteOutput copies the build artifact at src to a destination
// declared by an `output.*` entry in wasmify.json. A relative dst is
// resolved against baseDir (the directory that holds wasmify.json);
// an absolute dst is used unchanged. Intermediate directories are
// created. It returns the resolved destination path.
func WriteOutput(src, dst, baseDir string) (string, error) {
	if !filepath.IsAbs(dst) {
		dst = filepath.Join(baseDir, dst)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("read artifact %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return "", fmt.Errorf("write output %s: %w", dst, err)
	}
	return dst, nil
}
