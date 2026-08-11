package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type opencodeBootstrapper struct {
	catalog    opencodeCatalog
	downloader opencodeDownloader
	archive    opencodeArchive
}

func (bootstrapper opencodeBootstrapper) newOpenCodeBootstrapper() opencodeBootstrapper {
	return opencodeBootstrapper{
		catalog: opencodeCatalog{}, downloader: (opencodeDownloader{}).newOpenCodeDownloader(), archive: (opencodeArchive{}).newOpenCodeArchive(),
	}
}

func (bootstrapper opencodeBootstrapper) Install(ctx context.Context, root string) (string, error) {
	rootPackage := bootstrapper.catalog.RootPackage()
	rootArchive, err := bootstrapper.downloader.Download(ctx, root, rootPackage)
	if err != nil {
		return "", err
	}
	if err = bootstrapper.archive.ValidateRoot(rootArchive, rootPackage); err != nil {
		return "", err
	}
	platformPackage, err := bootstrapper.catalog.PlatformPackage(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	platformArchive, err := bootstrapper.downloader.Download(ctx, root, platformPackage)
	if err != nil {
		return "", err
	}
	executable := filepath.Join(root, filepath.Base(platformPackage.ExecutableEntry))
	if err = bootstrapper.archive.ExtractExecutable(platformArchive, executable, platformPackage); err != nil {
		return "", err
	}
	if runtime.GOOS != "windows" {
		if err = os.Chmod(executable, 0o700); err != nil { // #nosec G302 -- executable is private and owner-executable.
			return "", fmt.Errorf("protect OpenCode executable: %w", err)
		}
	}
	version, err := (opencodeCommand{}).boundedCommandOutput(
		ctx, root, (opencodeEnvironment{}).minimumEvaluationEnvironment(root), maximumOpenCodeDiagnosticBytes, executable, "--version",
	)
	if err != nil {
		return "", fmt.Errorf("execute reviewed OpenCode package: %w", err)
	}
	if strings.TrimSpace(version) != openCodeVersion {
		return "", errors.New("OpenCode executable version differs from the reviewed package")
	}
	return executable, nil
}
