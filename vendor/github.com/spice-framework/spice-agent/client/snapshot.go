package client

import (
	"errors"
	"fmt"
	"slices"
)

// Snapshot is a bounded opaque authenticated snapshot envelope. Its contents
// are deliberately not interpreted by clients; an authorized Session exports
// and imports the bytes atomically.
type Snapshot struct{ encoded []byte }

// ParseSnapshot constructs an opaque transfer value from persisted bytes.
// Cryptographic and lifecycle verification remains the importing authority's
// responsibility and must occur before state mutation.
func ParseSnapshot(encoded []byte) (Snapshot, error) {
	if len(encoded) == 0 || len(encoded) > MaximumSnapshotEnvelopeBytes {
		return Snapshot{}, fmt.Errorf("client snapshot must be between 1 and %d bytes", MaximumSnapshotEnvelopeBytes)
	}
	return Snapshot{encoded: slices.Clone(encoded)}, nil
}

// MarshalBinary returns a defensive copy suitable for persistence or transfer.
func (snapshot Snapshot) MarshalBinary() ([]byte, error) {
	if len(snapshot.encoded) == 0 || len(snapshot.encoded) > MaximumSnapshotEnvelopeBytes {
		return nil, errors.New("client snapshot is invalid")
	}
	return slices.Clone(snapshot.encoded), nil
}

func (snapshot Snapshot) SizeBytes() int { return len(snapshot.encoded) }
