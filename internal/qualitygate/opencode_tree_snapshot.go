package main

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
)

type opencodeTreeSnapshot struct {
	Digest [sha256.Size]byte
	Files  map[string][sha256.Size]byte
}

func (snapshot opencodeTreeSnapshot) HexDigest() string {
	return hex.EncodeToString(snapshot.Digest[:])
}

func (snapshot opencodeTreeSnapshot) Changes(after opencodeTreeSnapshot) []string {
	paths := make([]string, 0)
	for path, digest := range snapshot.Files {
		if candidate, exists := after.Files[path]; !exists || candidate != digest {
			paths = append(paths, path)
		}
	}
	for path := range after.Files {
		if _, exists := snapshot.Files[path]; !exists {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}
