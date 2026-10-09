package online

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

type applyJournal struct {
	Version    int                        `json:"version"`
	Salt       string                     `json:"salt"`
	Scope      string                     `json:"scope"`
	Corpora    map[string]*corpusEntry    `json:"corpora"`
	Connectors map[string]*connectorEntry `json:"connectors"`
	Plugins    map[string]*pluginEntry    `json:"plugins"`
}
type corpusEntry struct {
	ID          string       `json:"id,omitempty"`
	Declaration sourceCorpus `json:"declaration"`
}
type connectorEntry struct {
	ID                string          `json:"id,omitempty"`
	Declaration       sourceConnector `json:"declaration"`
	Digest            string          `json:"credential_digest,omitempty"`
	CredentialVersion int             `json:"credential_version,omitempty"`
}
type pluginEntry struct {
	ID     string `json:"id,omitempty"`
	Digest string `json:"request_digest"`
}
type journalFile struct {
	path    string
	lock    *os.File
	journal applyJournal
}

func randomApplyKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func (j *applyJournal) digest(v any) string {
	b, _ := json.Marshal(v)
	mac := hmac.New(sha256.New, []byte(j.Salt))
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))
}
func equalApply(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func privateJournalPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("journal must be a private regular file (0600)")
	}
	return nil
}
func openApplyJournal(path, scope string) (*journalFile, error) {
	fail := func() (*journalFile, error) {
		return nil, usageError("cannot open private replay journal; check permissions and deployment binding")
	}
	if privateJournalPath(path) != nil || privateJournalPath(path+".lock") != nil {
		return fail()
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fail()
	}
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		lock.Close()
		return nil, usageError("replay journal is already in use")
	}
	j := &journalFile{path: path, lock: lock}
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) > 8<<20 || json.Unmarshal(data, &j.journal) != nil || j.journal.Version != 1 || len(j.journal.Salt) != 64 {
			j.close()
			return fail()
		}
	} else if errors.Is(err, os.ErrNotExist) {
		salt, err := randomApplyKey()
		if err != nil {
			j.close()
			return fail()
		}
		j.journal = applyJournal{Version: 1, Salt: salt, Corpora: map[string]*corpusEntry{}, Connectors: map[string]*connectorEntry{}, Plugins: map[string]*pluginEntry{}}
	} else {
		j.close()
		return fail()
	}
	expected := j.journal.digest(scope)
	if j.journal.Scope != "" && j.journal.Scope != expected {
		j.close()
		return fail()
	}
	j.journal.Scope = expected
	if j.journal.Corpora == nil || j.journal.Connectors == nil || j.journal.Plugins == nil {
		j.close()
		return fail()
	}
	return j, nil
}
func (j *journalFile) close() { syscall.Flock(int(j.lock.Fd()), syscall.LOCK_UN); j.lock.Close() }
func (j *journalFile) save() error {
	data, err := json.MarshalIndent(j.journal, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(j.path), ".quivr-apply-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), j.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(j.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
