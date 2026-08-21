package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// SchemaVersion is stamped into every file this package writes. A file carrying
// a higher version was written by a newer binary; readers ignore it rather than
// misinterpreting it.
const SchemaVersion = 1

// envelope wraps persisted data so the version travels with it.
type envelope struct {
	SchemaVersion int             `json:"schemaVersion"`
	Data          json.RawMessage `json:"data"`
}

// ErrSchemaTooNew reports a file written by a newer binary. Callers treat this
// as "start from defaults", never as a fatal error — a rolled-back binary must
// still start.
var ErrSchemaTooNew = errors.New("file schema is newer than this binary understands")

// WriteJSONAtomic writes v to path so that a crash can never leave a truncated
// file. It writes a sibling temp file, fsyncs it, renames over the target, then
// fsyncs the directory so the rename itself is durable.
//
// Skipping the directory fsync is the common mistake: the rename can otherwise
// be lost on power failure even though the data was synced.
func WriteJSONAtomic(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filepath.Base(path), err)
	}
	body, err := json.Marshal(envelope{SchemaVersion: SchemaVersion, Data: data})
	if err != nil {
		return fmt.Errorf("marshal envelope %s: %w", filepath.Base(path), err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	// Any failure past this point must not leave the temp file behind.
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename onto %s: %w", path, err)
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s for sync: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return nil
}

// ReadJSON loads path into v.
//
// A missing file returns (false, nil): "nothing persisted yet" is the normal
// first-run case, not an error. A present-but-unreadable file returns an error
// and is quarantined by the caller via Quarantine.
func ReadJSON(path string, v any) (found bool, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return true, fmt.Errorf("parse %s: %w", path, err)
	}
	if env.SchemaVersion > SchemaVersion {
		return true, fmt.Errorf("%s: %w (file v%d, binary v%d)",
			path, ErrSchemaTooNew, env.SchemaVersion, SchemaVersion)
	}
	if len(env.Data) == 0 {
		return true, fmt.Errorf("parse %s: envelope has no data", path)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		return true, fmt.Errorf("parse %s data: %w", path, err)
	}
	return true, nil
}

// Quarantine renames a file the reader could not understand, so the next start
// begins from defaults without silently destroying whatever was there. Losing
// state must never be silent, and it must never be fatal either.
func Quarantine(path string) (newPath string, err error) {
	newPath = path + ".corrupt"
	if err := os.Rename(path, newPath); err != nil {
		return "", fmt.Errorf("quarantine %s: %w", path, err)
	}
	return newPath, nil
}
