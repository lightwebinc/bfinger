package owner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// BackupFile is the backup mark's file name under the home directory.
const BackupFile = "backup.json"

// Backup records the owner's last backup of the home: when it was taken and
// which witness it holds. A copy of the home is only as good as its witness.
// Every transition reveals the witness the previous record committed to, so
// a copy taken before the latest transition holds a witness that is already
// public and opens nothing: restored, it can never publish again. The mark
// is what lets doctor say so before the copy is needed.
//
// The witness itself is never written here. Mark is a domain-separated hash
// of it, which identifies the witness without being one, so the mark is safe
// to keep beside the copy or anywhere else.
type Backup struct {
	At   time.Time `json:"at"`
	Seq  uint64    `json:"seq"`
	Mark string    `json:"witnessMark"`
}

// WitnessMark is the backup mark of a witness: SHA-256 of a domain string
// and the witness bytes, hex. It is not the witness commitment (wc, plain
// SHA-256 of the witness), so a mark cannot be matched against a record.
func WitnessMark(witnessHex string) (string, error) {
	w, err := hex.DecodeString(witnessHex)
	if err != nil || len(w) == 0 {
		return "", fmt.Errorf("witness: not hex")
	}
	h := sha256.Sum256(append([]byte("bfinger backup mark v1\x00"), w...))
	return hex.EncodeToString(h[:]), nil
}

// LoadBackup reads the backup mark; a missing file is (nil, nil): no backup
// was ever recorded.
func LoadBackup(dir string) (*Backup, error) {
	raw, err := os.ReadFile(filepath.Join(dir, BackupFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var b Backup
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("%s: %w", BackupFile, err)
	}
	return &b, nil
}

// RecordBackup records that the home was just copied, as the state stands
// now. It refuses a home that has never published: there is no witness to
// keep yet, and the key alone is backed up by copying identity.json.
func RecordBackup(dir string, s *State, now time.Time) (*Backup, error) {
	if s == nil || s.WitnessHex == "" {
		return nil, errors.New("this home has never published, so there is no witness to back up yet; copy identity.json")
	}
	mark, err := WitnessMark(s.WitnessHex)
	if err != nil {
		return nil, err
	}
	b := &Backup{At: now.UTC(), Seq: s.Seq, Mark: mark}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".backup-*")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(append(raw, '\n'))
		if err == nil {
			err = syncFile(tmp)
		}
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
	} else {
		tmp.Close()
	}
	if err != nil {
		os.Remove(name)
		return nil, err
	}
	if err := os.Rename(name, filepath.Join(dir, BackupFile)); err != nil {
		os.Remove(name)
		return nil, err
	}
	return b, syncDir(dir)
}

// Current reports whether the recorded backup holds the state's witness,
// which is the one thing a copy of the home cannot get back from anywhere
// else.
func (b *Backup) Current(s *State) bool {
	if b == nil || s == nil || s.WitnessHex == "" {
		return false
	}
	mark, err := WitnessMark(s.WitnessHex)
	return err == nil && mark == b.Mark
}
