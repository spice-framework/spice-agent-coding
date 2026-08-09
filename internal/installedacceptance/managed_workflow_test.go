package installedacceptance

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

// TestInstalledManagedAttachOrStart proves the zero-argument distribution
// contract with the real acceptance-tagged binaries. An absent daemon is
// started from the terminal binary's sibling and remains terminal-owned; an
// already published compatible daemon remains externally owned.
func TestInstalledManagedAttachOrStart(t *testing.T) {
	root := repositoryRoot(t)
	daemonBinary, terminalBinary := buildApplications(t, root)
	assertVersion(t, daemonBinary, "spice-agentd")
	assertVersion(t, terminalBinary, "spice-agent")

	t.Run("starts and cleans up an owned sibling daemon", func(t *testing.T) {
		fixture := newManagedFixture(t, daemonBinary, terminalBinary)
		terminal := startProcess(t, terminalBinary, nil, fixture.environment)
		t.Cleanup(func() { terminal.stop(t, false) })
		terminal.waitForOutput(t, "[READY]")
		metadata := waitForManagedEndpoint(t, fixture.store, terminal)
		assertManagedBuild(t, metadata)
		if int(metadata.Process().ID()) == terminal.command.Process.Pid {
			t.Fatalf("managed daemon reused terminal PID %d", terminal.command.Process.Pid)
		}
		witness, err := openManagedProcessWitness(metadata.Process().ID())
		if err != nil {
			t.Fatalf("observe owned managed daemon %d: %v", metadata.Process().ID(), err)
		}
		defer func() {
			if closeErr := witness.Close(); closeErr != nil {
				t.Errorf("close owned managed daemon witness: %v", closeErr)
			}
		}()

		terminal.stop(t, true)
		waitForManagedProcessExit(t, witness, metadata, terminal)
		waitForEndpointAbsence(t, fixture.store, terminal)
	})

	t.Run("attaches to and preserves an externally owned daemon", func(t *testing.T) {
		fixture := newManagedFixture(t, daemonBinary, terminalBinary)
		daemon := startProcess(t, daemonBinary, []string{"serve"}, fixture.environment)
		t.Cleanup(func() { daemon.stop(t, false) })
		published := waitForEndpoint(t, fixture.store, daemon, nil, "")
		assertManagedBuild(t, published)

		terminal := startProcess(t, terminalBinary, nil, fixture.environment)
		t.Cleanup(func() { terminal.stop(t, false) })
		terminal.waitForOutput(t, "[READY]")
		attached := waitForManagedEndpoint(t, fixture.store, terminal)
		assertSameManagedProcess(t, attached, published)

		terminal.stop(t, true)
		daemon.assertRunning(t)
		remaining, err := fixture.store.Discover(t.Context())
		if err != nil {
			t.Fatalf("discover externally owned daemon after terminal exit: %v", err)
		}
		assertSameManagedProcess(t, remaining, published)

		daemon.stop(t, true)
		waitForEndpointAbsence(t, fixture.store, daemon)
	})
}

type managedFixture struct {
	store       *endpoint.Store
	environment map[string]string
}

func newManagedFixture(t *testing.T, daemonBinary, terminalBinary string) managedFixture {
	t.Helper()
	if filepath.Dir(daemonBinary) != filepath.Dir(terminalBinary) {
		t.Fatalf("managed binaries are not siblings: %q and %q", daemonBinary, terminalBinary)
	}
	currentScope, err := endpoint.CurrentUserScope()
	if err != nil {
		t.Fatal(err)
	}
	scopeDirectory := filepath.Join(
		currentScope.Directory(),
		fmt.Sprintf("installed-managed-%d-%d", os.Getpid(), time.Now().UnixNano()),
	)
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(scopeDirectory); removeErr != nil { // #nosec G703 -- exact test-owned child of the validated user scope.
			t.Error(removeErr)
		}
	})
	store, err := endpoint.OpenStore(endpoint.StoreConfig{
		Directory: scopeDirectory, PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if _, discoverErr := store.Discover(t.Context()); !errors.Is(discoverErr, endpoint.ErrNotFound) {
		t.Fatalf("inspect private managed endpoint: %v", discoverErr)
	}

	workspace := t.TempDir()
	authority := filepath.Join(
		scopeDirectory,
		fmt.Sprintf("installed-managed-authority-%d-%d", os.Getpid(), time.Now().UnixNano()),
	)
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(authority); removeErr != nil { // #nosec G703 -- exact test-owned child of the validated user scope.
			t.Error(removeErr)
		}
	})
	return managedFixture{
		store: store,
		environment: map[string]string{
			"GOWORK":                                 "off",
			"OPENAI_API_KEY":                         acceptanceAPIKey,
			"OPENAI_BASE_URL":                        "https://127.0.0.1:1/v1",
			"OPENAI_MODEL":                           "installed-managed-model",
			"OPENAI_MAX_RETRIES":                     "0",
			"OPENAI_TIMEOUT":                         "5s",
			"SPICE_AGENT_WORKSPACE":                  workspace,
			"SPICE_AGENT_RUN_AUTHORITY_DIRECTORY":    authority,
			"SPICE_AGENT_ACCEPTANCE_SCOPE_DIRECTORY": scopeDirectory,
			"SPICE_AGENT_TERMINAL_ACCESSIBLE":        "true",
		},
	}
}

func waitForManagedEndpoint(t *testing.T, store *endpoint.Store, owner *process) endpoint.Metadata {
	t.Helper()
	var result endpoint.Metadata
	var lastErr error
	waitFor(t, func() bool {
		result, lastErr = store.Discover(t.Context())
		return lastErr == nil
	}, func() string {
		return fmt.Sprintf(
			"managed endpoint was not published: %v\nstdout:\n%s\nstderr:\n%s",
			lastErr, owner.stdout.String(), owner.stderr.String(),
		)
	})
	return result
}

func waitForEndpointAbsence(t *testing.T, store *endpoint.Store, owner *process) {
	t.Helper()
	var lastErr error
	waitFor(t, func() bool {
		_, lastErr = store.Discover(t.Context())
		return errors.Is(lastErr, endpoint.ErrNotFound)
	}, func() string {
		return fmt.Sprintf(
			"managed endpoint remained after owner exit: %v\nstdout:\n%s\nstderr:\n%s",
			lastErr, owner.stdout.String(), owner.stderr.String(),
		)
	})
}

func waitForManagedProcessExit(
	t *testing.T,
	witness *managedProcessWitness,
	metadata endpoint.Metadata,
	owner *process,
) {
	t.Helper()
	var lastErr error
	waitFor(t, func() bool {
		var exited bool
		exited, lastErr = witness.Exited()
		return lastErr == nil && exited
	}, func() string {
		return fmt.Sprintf(
			"owned managed daemon %d/%x remained alive: %v\nterminal stdout:\n%s\nterminal stderr:\n%s",
			metadata.Process().ID(), metadata.Process().InstanceID(), lastErr,
			owner.stdout.String(), owner.stderr.String(),
		)
	})
}

func assertManagedBuild(t *testing.T, metadata endpoint.Metadata) {
	t.Helper()
	if metadata.Server().Version() != acceptanceVersion || metadata.Server().Commit() != acceptanceCommit {
		t.Fatalf(
			"managed daemon advertised build = %q %q",
			metadata.Server().Version(), metadata.Server().Commit(),
		)
	}
}

func assertSameManagedProcess(t *testing.T, got, want endpoint.Metadata) {
	t.Helper()
	if got.Process().ID() != want.Process().ID() ||
		!bytes.Equal(got.Process().InstanceID(), want.Process().InstanceID()) ||
		got.Address() != want.Address() {
		t.Fatalf(
			"managed endpoint process = %d/%x at %q, want %d/%x at %q",
			got.Process().ID(), got.Process().InstanceID(), got.Address(),
			want.Process().ID(), want.Process().InstanceID(), want.Address(),
		)
	}
}
