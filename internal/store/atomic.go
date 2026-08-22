package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const SchemaVersion = 1

type envelope struct {
	SchemaVersion int             `json:"schemaVersion"`
	Data          json.RawMessage `json:"data"`
}

// ErrSchemaTooNew means a newer binary wrote the file. Callers start from
// defaults; a rolled-back binary must still run.
var ErrSchemaTooNew = errors.New("file schema is newer than this binary understands")

// WriteJSONAtomic writes via temp file, fsync, rename, then fsyncs the
// directory. The directory fsync is the commonly skipped part: without it the
// rename can be lost on power failure even though the data was synced.
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

// ReadJSON returns (false, nil) for a missing file: nothing persisted yet is the
// normal first run, not an error.
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

// Quarantine moves aside a file the reader could not understand. Losing state
// must be neither silent nor fatal.
func Quarantine(path string) (newPath string, err error) {
	newPath = path + ".corrupt"
	if err := os.Rename(path, newPath); err != nil {
		return "", fmt.Errorf("quarantine %s: %w", path, err)
	}
	return newPath, nil
}
