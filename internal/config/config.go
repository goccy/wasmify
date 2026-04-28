package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type ProjectEntry struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

type Config struct {
	Projects []ProjectEntry `json:"projects"`
}

func configDir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "wasmify"), nil
}

func ConfigFilePath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func Load() (*Config, error) {
	path, err := ConfigFilePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	return &cfg, nil
}

func (c *Config) Save() error {
	path, err := ConfigFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

func hashPath(projectPath string) string {
	h := sha256.Sum256([]byte(projectPath))
	return fmt.Sprintf("%x", h)
}

// ProjectDir returns the data directory for the given project path,
// creating it and registering in config.json if it doesn't exist yet.
func (c *Config) ProjectDir(projectPath string) (string, error) {
	absPath, err := filepath.Abs(projectPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve absolute path: %w", err)
	}

	hash := hashPath(absPath)

	// Check if already registered
	for _, p := range c.Projects {
		if p.Hash == hash {
			dir, err := projectDataDir(hash)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("failed to create project directory: %w", err)
			}
			return dir, nil
		}
	}

	// Register new project
	c.Projects = append(c.Projects, ProjectEntry{
		Path: absPath,
		Hash: hash,
	})
	if err := c.Save(); err != nil {
		return "", err
	}

	dir, err := projectDataDir(hash)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create project directory: %w", err)
	}
	return dir, nil
}

func projectDataDir(hash string) (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "projects", hash), nil
}
