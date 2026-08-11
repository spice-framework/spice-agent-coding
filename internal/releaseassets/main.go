// Command releaseassets deterministically renders release-owned notices and
// protocol descriptors from the exact committed vendor and Protobuf graphs.
package main

import "os"

func main() {
	os.Exit((releaseAssetsCommand{}).run(os.Args[1:], os.Stdout, os.Stderr)) //nolint:forbidigo // Command entrypoint propagates the renderer outcome.
}
