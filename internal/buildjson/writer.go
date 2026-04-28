package buildjson

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func Load(projectDir string) (*BuildJSON, error) {
	path := filepath.Join(projectDir, "build.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read build.json: %w", err)
	}
	var b BuildJSON
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("failed to parse build.json: %w", err)
	}
	return &b, nil
}

func Save(projectDir string, b *BuildJSON) error {
	path := filepath.Join(projectDir, "build.json")
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal build.json: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}
