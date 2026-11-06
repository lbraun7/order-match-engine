package eventlog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReplicatedLogRepairsMissingTruncatedAndCorruptCopies(t *testing.T) {
	directory := t.TempDir()
	primary := filepath.Join(directory, "primary", "events.log")
	replica := filepath.Join(directory, "replica", "events.log")
	log, err := OpenReplicated(primary, replica)
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(1); sequence <= 3; sequence++ {
		if err := log.Append(Event{Sequence: sequence, Type: TypePlace, Payload: []byte{byte(sequence)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(primary); err != nil {
		t.Fatal(err)
	}
	log, err = OpenReplicated(primary, replica)
	if err != nil {
		t.Fatalf("repair missing primary: %v", err)
	}
	if events, err := log.ReadAll(); err != nil || len(events) != 3 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(replica)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replica, data[:len(data)-1], 0o600); err != nil {
		t.Fatal(err)
	}
	log, err = OpenReplicated(primary, replica)
	if err != nil {
		t.Fatalf("repair truncated replica: %v", err)
	}
	_ = log.Close()

	data, _ = os.ReadFile(primary)
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(primary, data, 0o600); err != nil {
		t.Fatal(err)
	}
	log, err = OpenReplicated(primary, replica)
	if err != nil {
		t.Fatalf("repair corrupt primary: %v", err)
	}
	_ = log.Close()

	events, err := ReadFile(replica)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewrite(replica, events[:2]); err != nil {
		t.Fatal(err)
	}
	log, err = OpenReplicated(primary, replica)
	if err != nil {
		t.Fatalf("repair valid strict prefix: %v", err)
	}
	events, err = log.ReadAll()
	if err != nil || len(events) != 3 {
		t.Fatalf("reconciled events=%d err=%v", len(events), err)
	}
	_ = log.Close()
}

func TestReplicatedLogRejectsDivergentHistories(t *testing.T) {
	directory := t.TempDir()
	primaryPath := filepath.Join(directory, "primary.log")
	replicaPath := filepath.Join(directory, "replica.log")
	for path, payload := range map[string]string{primaryPath: "primary", replicaPath: "replica"} {
		log, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := log.Append(Event{Sequence: 1, Type: TypePlace, Payload: []byte(payload)}); err != nil {
			t.Fatal(err)
		}
		_ = log.Close()
	}
	if _, err := OpenReplicated(primaryPath, replicaPath); !errors.Is(err, ErrReplicaDiverged) {
		t.Fatalf("error = %v, want replica divergence", err)
	}
}

func TestReplicatedLogBecomesUnusableAfterOneCopyFails(t *testing.T) {
	directory := t.TempDir()
	log, err := OpenReplicated(filepath.Join(directory, "primary.log"), filepath.Join(directory, "replica.log"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.replica.Close(); err != nil {
		t.Fatal(err)
	}
	if err := log.Append(Event{Sequence: 1, Type: TypePlace, Payload: []byte(`{}`)}); err == nil {
		t.Fatal("append succeeded with closed replica")
	}
	if err := log.Append(Event{Sequence: 1, Type: TypePlace, Payload: []byte(`{}`)}); !errors.Is(err, ErrLogFailed) {
		t.Fatalf("second append error = %v, want ErrLogFailed", err)
	}
	_ = log.primary.Close()
}
