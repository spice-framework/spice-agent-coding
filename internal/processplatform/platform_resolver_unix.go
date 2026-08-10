//go:build linux || darwin

package processplatform

import (
	"os"
)

type platformResolver struct{}

func (platformResolver) environmentNameEqual(left, right string) bool { return left == right }

func (platformResolver) executableExtensions(string, []string) []string { return []string{""} }

func (platformResolver) executable(info os.FileInfo) bool { return info.Mode().Perm()&0o111 != 0 }
