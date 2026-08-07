//go:build windows

package localipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const (
	windowsPipePrefix            = `\\.\pipe\`
	windowsSpicePipePrefix       = "spice-agent-"
	maximumWindowsPipeLength     = 256
	maximumWindowsPipeNameLength = 128
	pipeBufferBytes              = 64 << 10
)

// Listen creates a byte-mode local named pipe with a protected DACL granting
// generic access only to the current process user. go-winio creates every pipe
// instance with FILE_PIPE_REJECT_REMOTE_CLIENTS.
func Listen(address string) (net.Listener, error) {
	if err := validateWindowsAddress(address); err != nil {
		return nil, err
	}
	descriptor, err := currentUserSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	listener, err := winio.ListenPipe(address, &winio.PipeConfig{
		SecurityDescriptor: descriptor,
		InputBufferSize:    pipeBufferBytes,
		OutputBufferSize:   pipeBufferBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("listen on local IPC endpoint: %w", err)
	}
	return listener, nil
}

// Dial connects to the exact local named pipe and honors caller cancellation
// and deadlines. Remote UNC pipe addresses are rejected before any I/O.
func Dial(ctx context.Context, address string) (net.Conn, error) {
	if ctx == nil {
		return nil, errors.New("local IPC dial context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateWindowsAddress(address); err != nil {
		return nil, err
	}
	connection, err := winio.DialPipeContext(ctx, address)
	if err != nil {
		return nil, fmt.Errorf("dial local IPC endpoint: %w", err)
	}
	return connection, nil
}

func validateWindowsAddress(address string) error {
	if len(address) <= len(windowsPipePrefix) || len(address) > maximumWindowsPipeLength ||
		!strings.HasPrefix(address, windowsPipePrefix) || strings.TrimSpace(address) != address ||
		strings.IndexByte(address, 0) >= 0 || !safeWindowsPipeName(address[len(windowsPipePrefix):]) {
		return fmt.Errorf("%w: named pipe must use the canonical local \\.\\pipe\\spice-agent-name form", ErrUnsafeEndpoint)
	}
	return nil
}

func safeWindowsPipeName(name string) bool {
	if !strings.HasPrefix(name, windowsSpicePipePrefix) || len(name) == len(windowsSpicePipePrefix) ||
		len(name) > maximumWindowsPipeNameLength {
		return false
	}
	for _, current := range name {
		if current >= 'a' && current <= 'z' || current >= 'A' && current <= 'Z' ||
			current >= '0' && current <= '9' || strings.ContainsRune("._-", current) {
			continue
		}
		return false
	}
	return true
}

func currentUserSecurityDescriptor() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read current user for local IPC ACL: %w", err)
	}
	if user == nil || user.User.Sid == nil {
		return "", errors.New("current user SID is unavailable")
	}
	sid := user.User.Sid.String()
	if sid == "" {
		return "", errors.New("current user SID is invalid")
	}
	return "D:P(A;;GA;;;" + sid + ")", nil
}
