// Package eventlog implements the durable append-only command log.
package eventlog

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

const (
	Version        uint16 = 1
	headerSize            = 24
	maxPayloadSize        = 16 << 20
)

var (
	magic              = [4]byte{'O', 'M', 'E', 'L'}
	ErrCorruptRecord   = errors.New("corrupt event log record")
	ErrTruncatedRecord = errors.New("truncated event log record")
	ErrUnsupported     = errors.New("unsupported event log version")
	ErrLogFailed       = errors.New("event log is unusable after a write failure")
)

type Type uint8

const (
	TypePlace  Type = 1
	TypeCancel Type = 2
)

type Event struct {
	Sequence uint64
	Type     Type
	Version  uint16
	Payload  []byte
}

// Log appends checksummed records and flushes each accepted event to stable
// storage before returning.
type Log struct {
	file   *os.File
	failed error
}

func Open(path string) (*Log, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Log{file: file}, nil
}

func (l *Log) Append(event Event) error {
	if l.failed != nil {
		return fmt.Errorf("%v: %w", l.failed, ErrLogFailed)
	}
	if event.Version == 0 {
		event.Version = Version
	}
	if len(event.Payload) > maxPayloadSize {
		return fmt.Errorf("payload is %d bytes: %w", len(event.Payload), ErrCorruptRecord)
	}
	header := make([]byte, headerSize)
	copy(header[:4], magic[:])
	binary.BigEndian.PutUint16(header[4:6], event.Version)
	header[6] = byte(event.Type)
	binary.BigEndian.PutUint64(header[8:16], event.Sequence)
	binary.BigEndian.PutUint32(header[16:20], uint32(len(event.Payload)))
	checksum := crc32.NewIEEE()
	_, _ = checksum.Write(header[4:20])
	_, _ = checksum.Write(event.Payload)
	binary.BigEndian.PutUint32(header[20:24], checksum.Sum32())

	if err := writeFull(l.file, header); err != nil {
		l.failed = err
		return err
	}
	if err := writeFull(l.file, event.Payload); err != nil {
		l.failed = err
		return err
	}
	if err := l.file.Sync(); err != nil {
		l.failed = err
		return err
	}
	return nil
}

func (l *Log) ReadAll() ([]Event, error) {
	if _, err := l.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var events []Event
	for {
		header := make([]byte, headerSize)
		n, err := io.ReadFull(l.file, header)
		if errors.Is(err, io.EOF) && n == 0 {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("record %d header: %w", len(events), ErrTruncatedRecord)
		}
		if string(header[:4]) != string(magic[:]) {
			return nil, fmt.Errorf("record %d magic: %w", len(events), ErrCorruptRecord)
		}
		version := binary.BigEndian.Uint16(header[4:6])
		if version != Version {
			return nil, fmt.Errorf("record %d version %d: %w", len(events), version, ErrUnsupported)
		}
		length := binary.BigEndian.Uint32(header[16:20])
		if length > maxPayloadSize {
			return nil, fmt.Errorf("record %d payload length: %w", len(events), ErrCorruptRecord)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(l.file, payload); err != nil {
			return nil, fmt.Errorf("record %d payload: %w", len(events), ErrTruncatedRecord)
		}
		checksum := crc32.NewIEEE()
		_, _ = checksum.Write(header[4:20])
		_, _ = checksum.Write(payload)
		if checksum.Sum32() != binary.BigEndian.Uint32(header[20:24]) {
			return nil, fmt.Errorf("record %d checksum: %w", len(events), ErrCorruptRecord)
		}
		events = append(events, Event{Version: version, Type: Type(header[6]), Sequence: binary.BigEndian.Uint64(header[8:16]), Payload: payload})
	}
	_, err := l.file.Seek(0, io.SeekEnd)
	return events, err
}

func (l *Log) Close() error { return l.file.Close() }

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
