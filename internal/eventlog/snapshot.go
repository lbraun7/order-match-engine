package eventlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Snapshot struct {
	Version   uint16          `json:"version"`
	LogEvents uint64          `json:"log_events"`
	Sequence  uint64          `json:"sequence"`
	State     json.RawMessage `json:"state"`
}

func WriteSnapshot(path string, snapshot Snapshot) error {
	snapshot.Version = Version
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".snapshot-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func ReadSnapshot(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode snapshot: %w", err)
	}
	if snapshot.Version != Version {
		return Snapshot{}, fmt.Errorf("snapshot version %d: %w", snapshot.Version, ErrUnsupported)
	}
	return snapshot, nil
}
