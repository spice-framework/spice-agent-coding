package main

import (
	"context"
	"crypto/sha512"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type opencodeDownloader struct {
	client *http.Client
}

func newOpenCodeDownloader() opencodeDownloader {
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		panic("OpenCode evaluator requires the standard HTTP transport")
	}
	transport := baseTransport.Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	client := &http.Client{
		Timeout:   2 * time.Minute,
		Transport: transport,
		CheckRedirect: func(request *http.Request, prior []*http.Request) error {
			if len(prior) >= 3 || request.URL.Scheme != "https" || request.URL.Hostname() != "registry.npmjs.org" {
				return errors.New("OpenCode package redirect left the reviewed registry")
			}
			return nil
		},
	}
	return opencodeDownloader{client: client}
}

func (downloader opencodeDownloader) Download(
	ctx context.Context,
	root string,
	specification opencodePackage,
) (path string, downloadErr error) {
	if !filepath.IsAbs(root) || specification.Name == "" || specification.Integrity == "" {
		return "", errors.New("OpenCode download requires an absolute root and exact package identity")
	}
	address := "https://registry.npmjs.org/" + url.PathEscape(specification.Name) + "/-/" + specification.Name + "-" + openCodeVersion + ".tgz"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", fmt.Errorf("construct OpenCode package request: %w", err)
	}
	response, err := downloader.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download OpenCode package: %w", err)
	}
	defer func() {
		downloadErr = errors.Join(downloadErr, response.Body.Close())
	}()
	if response.StatusCode != http.StatusOK || response.ContentLength > maximumOpenCodePackageBytes {
		return "", fmt.Errorf("download OpenCode package returned HTTP %d", response.StatusCode)
	}
	path = filepath.Join(root, specification.Name+"-"+openCodeVersion+".tgz")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- path is under the owned workspace.
	if err != nil {
		return "", fmt.Errorf("create OpenCode package: %w", err)
	}
	digest := sha512.New()
	written, copyErr := io.Copy(io.MultiWriter(file, digest), io.LimitReader(response.Body, maximumOpenCodePackageBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written == 0 || written > maximumOpenCodePackageBytes {
		return "", errors.Join(errors.New("download bounded OpenCode package"), copyErr, closeErr)
	}
	actual := "sha512-" + base64.StdEncoding.EncodeToString(digest.Sum(nil))
	if !strings.EqualFold(actual, specification.Integrity) {
		return "", errors.New("OpenCode package integrity differs from the reviewed digest")
	}
	return path, nil
}
