package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Seen records what the user last saw of each issue on one dashboard
// (host + group), so update tags survive restarts. Times are the server's
// updated_at values; WrittenAt is informational only and never compared.
type Seen struct {
	Host      string            `json:"host"`
	Group     string            `json:"group"`
	WrittenAt time.Time         `json:"written_at"`
	Covered   []string          `json:"covered,omitempty"` // issue states ("opened", "closed") whose lists were compared
	Issues    map[int]SeenIssue `json:"issues"`            // by global issue ID
}

// SeenIssue is the last seen version of one issue.
type SeenIssue struct {
	UpdatedAt time.Time `json:"updated_at"`
	Notes     int       `json:"notes"`
	State     string    `json:"state,omitempty"`  // "opened" or "closed", for pruning
	Tagged    bool      `json:"tagged,omitempty"` // changed by someone else and not opened since
}

// SeenFile returns the seen file for a dashboard, or "" without a state
// directory. host and group are hashed into the name, so dashboards never
// mix and the name needs no escaping.
func SeenFile(host, group string) string {
	dir := Dir()
	if dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(host + "\n" + group))
	return filepath.Join(dir, "seen-"+hex.EncodeToString(sum[:8])+".json")
}

// LoadSeen reads a seen file. A missing file is not an error (existed is
// false); an unreadable or corrupt one is (existed is true).
func LoadSeen(name string) (s Seen, existed bool, err error) {
	if name == "" {
		return s, false, ErrNoDir
	}
	b, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return s, false, nil
	}
	if err != nil {
		return s, true, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return Seen{}, true, err
	}
	if s.Issues == nil {
		return Seen{}, true, errors.New("no issues in " + name)
	}
	return s, true, nil
}

// SaveSeen writes a seen file atomically, private to the user.
func SaveSeen(name string, s Seen) error {
	if name == "" {
		return ErrNoDir
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeAtomic(name, b)
}
