package eventlog

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
)

func FuzzReadAll(f *testing.F) {
	f.Add([]byte{})
	f.Add(encodedRecord(1, TypePlace, []byte(`{"OrderID":"one"}`)))
	f.Add(append(encodedRecord(1, TypePlace, []byte(`{}`)), encodedRecord(2, TypeCancel, []byte(`{}`))...))
	f.Add([]byte("OMEL\x00\x01"))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		events, err := readAll(bytes.NewReader(data))
		if err != nil {
			return
		}
		var payloadBytes int
		for _, event := range events {
			if event.Version != Version {
				t.Fatalf("accepted event version %d", event.Version)
			}
			if len(event.Payload) > maxPayloadSize {
				t.Fatalf("accepted payload with %d bytes", len(event.Payload))
			}
			payloadBytes += len(event.Payload)
		}
		if payloadBytes > len(data) {
			t.Fatalf("decoded %d payload bytes from %d input bytes", payloadBytes, len(data))
		}
	})
}

func encodedRecord(sequence uint64, eventType Type, payload []byte) []byte {
	header := make([]byte, headerSize)
	copy(header[:4], magic[:])
	binary.BigEndian.PutUint16(header[4:6], Version)
	header[6] = byte(eventType)
	binary.BigEndian.PutUint64(header[8:16], sequence)
	binary.BigEndian.PutUint32(header[16:20], uint32(len(payload)))
	checksum := crc32.NewIEEE()
	_, _ = checksum.Write(header[4:20])
	_, _ = checksum.Write(payload)
	binary.BigEndian.PutUint32(header[20:24], checksum.Sum32())
	return append(header, payload...)
}
