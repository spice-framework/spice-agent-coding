package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spice-framework/spice-agent-tui/internal/annotationtool"
	"github.com/spice-framework/spice/annotation/sdk/protocol"
)

func main() {
	// Process exit translation belongs only at this executable boundary.
	os.Exit(run(os.Args, os.Stdin, os.Stdout, os.Stderr)) //nolint:forbidigo // Go tool entrypoint must report protocol failure.
}

func run(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(arguments) != 2 || arguments[1] != "--spice-stdio" {
		if _, err := fmt.Fprintln(stderr, "spice-agent-tui-annotations requires --spice-stdio"); err != nil {
			return 1
		}
		return 2
	}
	if err := protocol.Serve(context.Background(), stdin, stdout, annotationtool.New()); err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "spice-agent-tui-annotations: %v\n", err); writeErr != nil {
			return 1
		}
		return 1
	}
	return 0
}
