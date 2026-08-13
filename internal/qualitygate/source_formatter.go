package main

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

// sourceFormatter owns source discovery, formatting, and application style checks.
type sourceFormatter struct{}

func (owner sourceFormatter) format(ctx context.Context, root string, write bool) error {
	files, err := (sourceFormatter{}).goFiles(root)
	if err != nil {
		return err
	}
	for _, name := range []string{"goimports", "gofumpt"} {
		executable, pathErr := (commandRunner{}).toolPath(ctx, root, name)
		if pathErr != nil {
			return pathErr
		}
		option := "-l"
		if write {
			option = "-w"
		}
		for _, batch := range (sourceFormatter{}).formattingBatches(option, files) {
			result, captureErr := (commandRunner{}).capture(ctx, root, executable, append([]string{option}, batch...)...)
			if captureErr != nil {
				return captureErr
			}
			if !write && strings.TrimSpace(result) != "" {
				return fmt.Errorf("%s requires formatting: %s", name, strings.Join(strings.Fields(result), ", "))
			}
		}
	}
	return nil
}

const (
	maximumFormattingBatchFiles = 128
	maximumFormattingBatchBytes = 12 << 10
)

func (owner sourceFormatter) formattingBatches(option string, files []string) [][]string {
	result := make([][]string, 0, (len(files)+maximumFormattingBatchFiles-1)/maximumFormattingBatchFiles)
	current := make([]string, 0, min(len(files), maximumFormattingBatchFiles))
	currentBytes := len(option)
	for _, file := range files {
		fileBytes := len(file) + 3 // argument separator plus a conservative pair of quotes.
		if len(current) > 0 && (len(current) == maximumFormattingBatchFiles ||
			currentBytes+fileBytes > maximumFormattingBatchBytes) {
			result = append(result, current)
			current = make([]string, 0, min(len(files), maximumFormattingBatchFiles))
			currentBytes = len(option)
		}
		current = append(current, file)
		currentBytes += fileBytes
	}
	if len(current) > 0 {
		result = append(result, current)
	}
	return result
}

func (owner sourceFormatter) checkStyle(ctx context.Context, root string) error {
	executable, err := (commandRunner{}).toolPath(ctx, root, "spicestyle")
	if err != nil {
		return err
	}
	return (commandRunner{}).command(
		ctx,
		root,
		(sourceFormatter{}).hostileStyleEnvironment(),
		executable,
		(sourceFormatter{}).styleArguments()...,
	)
}

func (owner sourceFormatter) styleArguments() []string {
	return []string{"--config=.spice/style.json", "./..."}
}

// hostileStyleEnvironment proves the pinned Toolchain owns every configured
// analysis selection and host annotation-tool launch. None of these ambient
// values may alter diagnostics or trigger a toolchain or module download.
func (owner sourceFormatter) hostileStyleEnvironment() map[string]string {
	return map[string]string{
		"CGO_ENABLED":  "1",
		"GOAMD64":      "v4",
		"GOARCH":       "386",
		"GOAUTH":       "netrc",
		"GOENV":        "off",
		"GOEXPERIMENT": "ambientexperiment",
		"GOFIPS140":    "latest",
		"GOFLAGS":      "-tags=ambient",
		"GOOS":         "plan9",
		"GOPROXY":      "http://127.0.0.1:1",
		"GOSUMDB":      "invalid.example",
		"GOTOOLCHAIN":  "go1.99.0+auto",
	}
}

func (owner sourceFormatter) goFiles(root string) ([]string, error) {
	result := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && path != root && slices.Contains([]string{".git", "tools", "vendor"}, entry.Name()) {
			return filepath.SkipDir
		}
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".go" {
			result = append(result, path)
		}
		return nil
	})
	slices.Sort(result)
	return result, err
}
