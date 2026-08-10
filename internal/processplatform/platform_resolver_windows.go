//go:build windows

package processplatform

import (
	"os"
	"path/filepath"
	"strings"
)

type platformResolver struct{}

func (platformResolver) environmentNameEqual(left, right string) bool {
	return strings.EqualFold(left, right)
}

func (platformResolver) executableExtensions(requested string, environment []string) []string {
	extension := filepath.Ext(requested)
	if extension != "" {
		if (platformResolver{}).shellScriptExtension(extension) {
			return nil
		}
		return []string{""}
	}
	pathExtensions, found := (&Resolver{}).environmentValue(environment, "PATHEXT")
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
		if (platformResolver{}).shellScriptExtension(extension) {
			continue
		}
		result = append(result, extension)
	}
	return append([]string{""}, result...)
}

func (platformResolver) shellScriptExtension(extension string) bool {
	return strings.EqualFold(extension, ".bat") || strings.EqualFold(extension, ".cmd")
}

func (platformResolver) executable(os.FileInfo) bool { return true }
