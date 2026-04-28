package wrapper

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// ParseLog reads a JSON Lines log file and returns all captured entries.
func ParseLog(logFile string) ([]LogEntry, error) {
	f, err := os.Open(logFile)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}
	defer func() { _ = f.Close() }()

	var entries []LogEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var entry LogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // Skip malformed lines
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read log file: %w", err)
	}
	return entries, nil
}
