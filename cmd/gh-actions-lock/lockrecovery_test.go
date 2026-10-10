package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/github/gh-actions-lock/internal/ghapi/httpmock"
	"github.com/github/gh-actions-lock/internal/pinpool"
	"github.com/github/gh-actions-lock/internal/resolve"
	"github.com/github/gh-actions-lock/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeConfirmer struct {
	result    bool
	err       error
	called    bool
	gotPrompt string
}

func (f *fakeConfirmer) Confirm(prompt string, _ bool) (bool, error) {
	f.called = true
	f.gotPrompt = prompt
	return f.result, f.err
}

func writeScratchLock(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "actions.lock")
	if err := os.WriteFile(p, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func discardUI() *ui.UI { return ui.NewWithWriter(io.Discard) }

func fileExists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Stat(p)
	return err == nil
}

func TestNewLockRecovery(t *testing.T) {
	parseErr := errors.New("missing required action field \"owner_id\"")

	t.Run("read-only/relock command refuses to delete and fails", func(t *testing.T) {
		lock := writeScratchLock(t)
		fc := &fakeConfirmer{result: true}
		rec := newLockRecovery(false, discardUI(), func() (confirmer, bool) { return fc, true }, false)

		recovered, err := rec(lock, parseErr)
		if recovered || err == nil {
			t.Fatalf("want (false, error), got (%v, %v)", recovered, err)
		}
		if fc.called {
			t.Error("confirmer must not be consulted when delete is disallowed")
		}
		if !fileExists(t, lock) {
			t.Error("lockfile must be left in place")
		}
	})

	t.Run("non-interactive flag fails without prompting", func(t *testing.T) {
		lock := writeScratchLock(t)
		fc := &fakeConfirmer{result: true}
		rec := newLockRecovery(true, discardUI(), func() (confirmer, bool) { return fc, true }, true)

		recovered, err := rec(lock, parseErr)
		if recovered || err == nil {
			t.Fatalf("want (false, error), got (%v, %v)", recovered, err)
		}
		if fc.called {
			t.Error("confirmer must not be consulted under --no-interactive")
		}
		if !fileExists(t, lock) {
			t.Error("lockfile must be left in place")
		}
	})

	t.Run("headless session (canPrompt false) fails", func(t *testing.T) {
		lock := writeScratchLock(t)
		rec := newLockRecovery(false, discardUI(), func() (confirmer, bool) { return nil, false }, true)

		recovered, err := rec(lock, parseErr)
		if recovered || err == nil {
			t.Fatalf("want (false, error), got (%v, %v)", recovered, err)
		}
		if !fileExists(t, lock) {
			t.Error("lockfile must be left in place")
		}
	})

	t.Run("interactive confirm stages recovery and preserves original", func(t *testing.T) {
		lock := writeScratchLock(t)
		fc := &fakeConfirmer{result: true}
		rec := newLockRecovery(false, discardUI(), func() (confirmer, bool) { return fc, true }, true)

		recovered, err := rec(lock, parseErr)
		if !recovered || err != nil {
			t.Fatalf("want (true, nil), got (%v, %v)", recovered, err)
		}
		if !fc.called {
			t.Error("confirmer should have been consulted")
		}
		content, readErr := os.ReadFile(lock)
		if readErr != nil {
			t.Fatalf("reading original lockfile after confirmation: %v", readErr)
		}
		if string(content) != "corrupt" {
			t.Error("confirmation must preserve original lockfile bytes until commit")
		}
	})

	t.Run("interactive confirm no fails and keeps file", func(t *testing.T) {
		lock := writeScratchLock(t)
		fc := &fakeConfirmer{result: false}
		rec := newLockRecovery(false, discardUI(), func() (confirmer, bool) { return fc, true }, true)

		recovered, err := rec(lock, parseErr)
		if recovered || err == nil {
			t.Fatalf("want (false, error), got (%v, %v)", recovered, err)
		}
		if !fileExists(t, lock) {
			t.Error("lockfile must be left in place when the user declines")
		}
	})

	t.Run("confirm error propagates and keeps file", func(t *testing.T) {
		lock := writeScratchLock(t)
		boom := errors.New("prompt failed")
		fc := &fakeConfirmer{err: boom}
		rec := newLockRecovery(false, discardUI(), func() (confirmer, bool) { return fc, true }, true)

		recovered, err := rec(lock, parseErr)
		if recovered || !errors.Is(err, boom) {
			t.Fatalf("want (false, boom), got (%v, %v)", recovered, err)
		}
		if !fileExists(t, lock) {
			t.Error("lockfile must be left in place on prompt error")
		}
	})
}

func TestCheck_ConfirmedRecoveryWithNoWorkflowsRemovesLockfile(t *testing.T) {
	for _, customDirectory := range []bool{false, true} {
		name := "default workflow directory"
		if customDirectory {
			name = "custom workflow directory"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			workflowsDir := filepath.Join(dir, ".github", "workflows")
			require.NoError(t, os.MkdirAll(workflowsDir, 0o755))
			lockPath := filepath.Join(workflowsDir, "actions.lock")
			require.NoError(t, os.WriteFile(lockPath, []byte("version: [broken\n"), 0o600))
			t.Chdir(dir)
			if customDirectory {
				t.Setenv("GH_ACTIONS_LOCK_WORKFLOWS_DIR", workflowsDir)
			} else {
				t.Setenv("GH_ACTIONS_LOCK_WORKFLOWS_DIR", "")
			}
			fc := &fakeConfirmer{result: true}
			oldFactory := confirmFactoryHook
			confirmFactoryHook = func() (confirmer, bool) { return fc, true }
			t.Cleanup(func() { confirmFactoryHook = oldFactory })
			reg := &httpmock.Registry{}
			defer reg.Verify(t)

			stdout, _, err := runCommandWithHTTP(t, reg, "--json=valid,workflows")
			require.NoError(t, err)
			assert.True(t, fc.called)
			assert.NoFileExists(t, lockPath)
			var payload struct {
				Valid     bool     `json:"valid"`
				Workflows []string `json:"workflows"`
			}
			require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
			assert.True(t, payload.Valid)
			assert.Empty(t, payload.Workflows)

			fc.called = false
			_, _, err = runCommandWithHTTP(t, reg, "--json=valid,workflows")
			require.ErrorContains(t, err, "no workflow files found")
			assert.False(t, fc.called, "the completed recovery must not prompt again")
		})
	}
}

func TestCheck_EmptyRecoveryKeepsLockfileWithoutDeletionAuthority(t *testing.T) {
	for _, tt := range []struct {
		name       string
		args       []string
		confirm    bool
		wantPrompt bool
	}{
		{name: "declined recovery", wantPrompt: true},
		{name: "no fix", args: []string{"--no-fix"}, confirm: true},
		{name: "verify", args: []string{"--verify"}, confirm: true},
		{name: "verify frozen", args: []string{"--verify-frozen"}, confirm: true},
		{name: "verify local", args: []string{"--verify-local"}, confirm: true},
		{name: "no interactive", args: []string{"--no-interactive"}, confirm: true},
		{name: "explicit empty directory", args: []string{".github/workflows"}, confirm: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			workflowsDir := filepath.Join(dir, ".github", "workflows")
			require.NoError(t, os.MkdirAll(workflowsDir, 0o755))
			lockPath := filepath.Join(workflowsDir, "actions.lock")
			before := []byte("version: [broken\n")
			require.NoError(t, os.WriteFile(lockPath, before, 0o600))
			t.Chdir(dir)
			t.Setenv("GH_ACTIONS_LOCK_WORKFLOWS_DIR", "")
			fc := &fakeConfirmer{result: tt.confirm}
			oldFactory := confirmFactoryHook
			confirmFactoryHook = func() (confirmer, bool) { return fc, true }
			t.Cleanup(func() { confirmFactoryHook = oldFactory })
			reg := &httpmock.Registry{}
			defer reg.Verify(t)

			_, _, err := runCommandWithHTTP(t, reg, tt.args...)
			require.Error(t, err)
			assert.Equal(t, tt.wantPrompt, fc.called)
			after, err := os.ReadFile(lockPath)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestCheck_EmptyRecoveryKeepsLockfileOnSetupFailure(t *testing.T) {
	dir := t.TempDir()
	workflowsDir := filepath.Join(dir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowsDir, 0o755))
	lockPath := filepath.Join(workflowsDir, "actions.lock")
	before := []byte("version: [broken\n")
	require.NoError(t, os.WriteFile(lockPath, before, 0o600))
	t.Chdir(dir)
	t.Setenv("GH_ACTIONS_LOCK_WORKFLOWS_DIR", "")
	fc := &fakeConfirmer{result: true}
	oldFactory := confirmFactoryHook
	confirmFactoryHook = func() (confirmer, bool) { return fc, true }
	t.Cleanup(func() { confirmFactoryHook = oldFactory })
	wantErr := errors.New("resolver unavailable")
	cmd := newRootCmd(func(string, *pinpool.Pool) (*resolve.Resolver, error) {
		return nil, wantErr
	})
	var output strings.Builder
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--json=valid,workflows"})

	assert.ErrorIs(t, cmd.Execute(), wantErr)
	assert.True(t, fc.called)
	after, err := os.ReadFile(lockPath)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}
