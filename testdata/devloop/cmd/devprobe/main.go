//go:build !spice_generate

package main

import (
	"os"

	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/devprobe"
)

func main() {
	os.Exit(spicegen.Main(os.Args[1:]))
}
