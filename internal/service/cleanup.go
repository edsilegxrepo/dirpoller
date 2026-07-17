package service

import (
	"os"
	"path/filepath"
	"time"
)

// purgeOldEntries deletes files/directories inside 'dir' whose modification time
// is older than 'cutoff', matching the criteria specified in the 'filter' function.
func purgeOldEntries(dir string, cutoff time.Time, filter func(entry os.DirEntry) bool) error {
	if dir == "" || dir == "." {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if filter(entry) {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
			}
		}
	}
	return nil
}
