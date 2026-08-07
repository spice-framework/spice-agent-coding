//go:build linux || darwin

package processplatform

import (
	"os"
)

func environmentNameEqual(left, right string) bool { return left == right }

func executableExtensions(string, []string) []string { return []string{""} }

func platformExecutable(info os.FileInfo) bool { return info.Mode().Perm()&0o111 != 0 }
