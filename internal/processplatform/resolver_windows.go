//go:build windows

package processplatform

import (
	"os"
	"path/filepath"
	"strings"
)

func environmentNameEqual(left, right string) bool { return strings.EqualFold(left, right) }

func executableExtensions(requested string, environment []string) []string {
	extension := filepath.Ext(requested)
	if extension != "" {
		if isShellScriptExtension(extension) {
			return nil
		}
		return []string{""}
	}
	pathExtensions, found := environmentValue(environment, "PATHEXT")
	if !found {
		return []string{""}
	}
	result := make([]string, 0, 8)
	for extension := range strings.SplitSeq(pathExtensions, ";") {
		if extension == "" {
			continue
		}
		if extension[0] != '.' {
			extension = "." + extension
		}
		if isShellScriptExtension(extension) {
			continue
		}
		result = append(result, extension)
	}
	return append([]string{""}, result...)
}

func isShellScriptExtension(extension string) bool {
	return strings.EqualFold(extension, ".bat") || strings.EqualFold(extension, ".cmd")
}

func platformExecutable(os.FileInfo) bool { return true }
