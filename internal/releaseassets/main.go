package main

import "os"

func main() {
	os.Exit((releaseAssetsCommand{}).run(os.Args[1:], os.Stdout, os.Stderr)) //nolint:forbidigo // Command entrypoint propagates the renderer outcome.
}
