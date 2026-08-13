package tuittest

import "bytes"

type virtualTerminalSegment struct {
	width  int
	height int
	output bytes.Buffer
}
