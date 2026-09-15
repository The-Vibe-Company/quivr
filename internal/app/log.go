package app

import (
	"os"
	"strconv"
	"sync"
)

// rotatingLog bounds each local process log to four files of at most 1 MiB.
// Log records contain bounded metadata rather than request bodies or credentials.
type rotatingLog struct {
	mu   sync.Mutex
	path string
}

func (l *rotatingLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	const limit = 1 << 20
	original := len(data)
	if len(data) > limit {
		data = append(append([]byte{}, data[:limit-1]...), '\n')
	}
	info, err := os.Stat(l.path)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	if info != nil && info.Size()+int64(len(data)) > limit {
		if err = os.Remove(l.path + ".3"); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		for i := 2; i >= 0; i-- {
			source := l.path
			if i > 0 {
				source += "." + strconv.Itoa(i)
			}
			if err = os.Rename(source, l.path+"."+strconv.Itoa(i+1)); err != nil && !os.IsNotExist(err) {
				return 0, err
			}
		}
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return 0, err
	}
	return original, nil
}
