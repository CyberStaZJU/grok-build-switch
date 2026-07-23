// Package recovery preserves unreadable persisted files before callers reset
// them to safe defaults.
package recovery

import (
	"fmt"
	"os"
	"time"
)

// BackupCorrupt renames path to a timestamped backup next to the original.
// The original extension is retained in the backup name so users can identify
// which store was recovered without opening the file.
func BackupCorrupt(path string) (string, error) {
	stamp := time.Now().Format("20060102-150405.000000000")
	for i := 0; i < 1000; i++ {
		backup := fmt.Sprintf("%s.corrupt-%s.bak", path, stamp)
		if i > 0 {
			backup = fmt.Sprintf("%s.corrupt-%s-%d.bak", path, stamp, i)
		}
		if _, err := os.Stat(backup); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if err := os.Rename(path, backup); err != nil {
			return "", err
		}
		if err := os.Chmod(backup, 0o600); err != nil {
			return "", err
		}
		return backup, nil
	}
	return "", fmt.Errorf("cannot allocate corrupt backup name for %s", path)
}
