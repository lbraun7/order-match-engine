package eventlog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLogRoundTripAndDetectsDamage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	log, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(Event{Sequence: 7, Type: TypePlace, Payload: []byte(`{"id":"one"}`)}); err != nil {
		t.Fatal(err)
	}
	events, err := log.ReadAll()
	if err != nil || len(events) != 1 || events[0].Sequence != 7 || string(events[0].Payload) != `{"id":"one"}` {
		t.Fatalf("events = %+v, error = %v", events, err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("checksum", func(t *testing.T) {
		damagedPath := filepath.Join(t.TempDir(), "events.log")
		damaged := append([]byte(nil), data...)
		damaged[len(damaged)-1] ^= 0xff
		if err := os.WriteFile(damagedPath, damaged, 0o600); err != nil {
			t.Fatal(err)
		}
		assertReadError(t, damagedPath, ErrCorruptRecord)
	})

	t.Run("truncated", func(t *testing.T) {
		truncatedPath := filepath.Join(t.TempDir(), "events.log")
		if err := os.WriteFile(truncatedPath, data[:len(data)-1], 0o600); err != nil {
			t.Fatal(err)
		}
		assertReadError(t, truncatedPath, ErrTruncatedRecord)
	})
}

func assertReadError(t *testing.T, path string, want error) {
	t.Helper()
	log, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if _, err := log.ReadAll(); !errors.Is(err, want) {
		t.Fatalf("ReadAll error = %v, want %v", err, want)
	}
}
