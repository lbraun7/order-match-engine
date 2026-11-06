package eventlog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrReplicaDiverged = errors.New("event log replicas diverged")
var errLogMissing = errors.New("event log missing")

// Writer is the persistence contract required by the matching engine.
type Writer interface {
	Append(Event) error
	ReadAll() ([]Event, error)
	Close() error
}

// ReplicatedLog flushes every record to two independent files before
// returning success. Startup repairs a missing, shorter, or corrupt copy from
// its valid peer, but refuses conflicting histories.
type ReplicatedLog struct {
	primary *Log
	replica *Log
	failed  error
}

func OpenReplicated(primaryPath, replicaPath string) (*ReplicatedLog, error) {
	if primaryPath == "" || replicaPath == "" || primaryPath == replicaPath {
		return nil, errors.New("distinct primary and replica log paths are required")
	}
	primaryEvents, primaryErr := readPath(primaryPath)
	replicaEvents, replicaErr := readPath(replicaPath)

	canonical, err := reconcile(primaryEvents, primaryErr, replicaEvents, replicaErr)
	if err != nil {
		return nil, err
	}
	if primaryErr != nil || !eventsEqual(primaryEvents, canonical) {
		if err := rewrite(primaryPath, canonical); err != nil {
			return nil, fmt.Errorf("repair primary log: %w", err)
		}
	}
	if replicaErr != nil || !eventsEqual(replicaEvents, canonical) {
		if err := rewrite(replicaPath, canonical); err != nil {
			return nil, fmt.Errorf("repair replica log: %w", err)
		}
	}
	primary, err := Open(primaryPath)
	if err != nil {
		return nil, err
	}
	replica, err := Open(replicaPath)
	if err != nil {
		primary.Close()
		return nil, err
	}
	return &ReplicatedLog{primary: primary, replica: replica}, nil
}

func (l *ReplicatedLog) Append(event Event) error {
	if l.failed != nil {
		return fmt.Errorf("%v: %w", l.failed, ErrLogFailed)
	}
	if err := l.primary.Append(event); err != nil {
		l.failed = fmt.Errorf("primary append: %w", err)
		return l.failed
	}
	if err := l.replica.Append(event); err != nil {
		l.failed = fmt.Errorf("replica append after primary commit: %w", err)
		return l.failed
	}
	return nil
}

func (l *ReplicatedLog) ReadAll() ([]Event, error) {
	primary, err := l.primary.ReadAll()
	if err != nil {
		return nil, err
	}
	replica, err := l.replica.ReadAll()
	if err != nil {
		return nil, err
	}
	if !eventsEqual(primary, replica) {
		return nil, ErrReplicaDiverged
	}
	return primary, nil
}

func (l *ReplicatedLog) Close() error {
	return errors.Join(l.primary.Close(), l.replica.Close())
}

func readPath(path string) ([]Event, error) {
	events, err := ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errLogMissing
	}
	return events, err
}

func reconcile(primary []Event, primaryErr error, replica []Event, replicaErr error) ([]Event, error) {
	switch {
	case errors.Is(primaryErr, errLogMissing) && errors.Is(replicaErr, errLogMissing):
		return nil, nil
	case errors.Is(primaryErr, errLogMissing) && replicaErr == nil:
		return replica, nil
	case errors.Is(replicaErr, errLogMissing) && primaryErr == nil:
		return primary, nil
	case primaryErr == nil && replicaErr == nil:
		if eventsEqual(primary, replica) {
			return primary, nil
		}
		if eventsPrefix(primary, replica) {
			return replica, nil
		}
		if eventsPrefix(replica, primary) {
			return primary, nil
		}
		return nil, ErrReplicaDiverged
	case primaryErr == nil:
		return primary, nil
	case replicaErr == nil:
		return replica, nil
	default:
		return nil, fmt.Errorf("primary error: %v; replica error: %v", primaryErr, replicaErr)
	}
}

func eventsPrefix(shorter, longer []Event) bool {
	return len(shorter) <= len(longer) && eventsEqual(shorter, longer[:len(shorter)])
}

func eventsEqual(left, right []Event) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Sequence != right[index].Sequence || left[index].Type != right[index].Type || left[index].Version != right[index].Version || !bytes.Equal(left[index].Payload, right[index].Payload) {
			return false
		}
	}
	return true
}

func rewrite(path string, events []Event) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".replica-repair-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
	log, err := Open(temporaryPath)
	if err != nil {
		return err
	}
	for _, event := range events {
		if err := log.Append(event); err != nil {
			log.Close()
			return err
		}
	}
	if err := log.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
