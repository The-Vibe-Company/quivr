package devhost

import (
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// ignoredDirs never trigger a restart: VCS data, virtual environments,
// dependency and build caches.
var ignoredDirs = map[string]bool{
	"__pycache__": true, "node_modules": true, "venv": true, "env": true,
	"build": true, "dist": true, "target": true,
}

type stamp struct {
	size int64
	mod  time.Time
}

// Watcher detects source changes under a plugin directory by polling file
// sizes and modification times, without an OS notification dependency.
type Watcher struct {
	root string
	last map[string]stamp
}

// NewWatcher records the current state of root.
func NewWatcher(root string) *Watcher {
	w := &Watcher{root: root}
	w.last = w.scan()
	return w
}

func (w *Watcher) scan() map[string]stamp {
	out := map[string]stamp{}
	_ = filepath.WalkDir(w.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != w.root && (strings.HasPrefix(name, ".") || ignoredDirs[name] || strings.HasSuffix(name, ".egg-info")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".pyc") {
			return nil
		}
		if info, err := d.Info(); err == nil {
			out[path] = stamp{size: info.Size(), mod: info.ModTime()}
		}
		return nil
	})
	return out
}

// Changed reports whether any watched file was added, removed or modified
// since the previous call (or NewWatcher).
func (w *Watcher) Changed() bool {
	now := w.scan()
	changed := len(now) != len(w.last)
	if !changed {
		for path, s := range now {
			if prev, ok := w.last[path]; !ok || prev != s {
				changed = true
				break
			}
		}
	}
	w.last = now
	return changed
}
