package main

import "os"

// asset is one deterministic release-owned file.
type asset struct {
	path    string
	content []byte
	mode    os.FileMode
}
