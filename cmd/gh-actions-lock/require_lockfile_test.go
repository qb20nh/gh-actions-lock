package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/github/gh-actions-lock/internal/ghapi/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const shellOnlyWorkflow = `
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: echo checked
`

const emptyDependencyLockfile = "version: v0.0.3\nworkflows: {}\ndependencies: {}\n"

func TestRequireLockfileRejectsMissingEmptyAndSymlinkedFiles(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
	}{
		{
			name: "missing lockfile",
			prepare: func(t *testing.T, lockPath string) {
				require.NoError(t, os.Remove(lockPath))
			},
		},
		{
			name: "empty lockfile",
			prepare: func(t *testing.T, lockPath string) {
				require.NoError(t, os.WriteFile(lockPath, nil, 0o600))
			},
		},
		{
			name: "whitespace lockfile",
			prepare: func(t *testing.T, lockPath string) {
				require.NoError(t, os.WriteFile(lockPath, []byte(" \t\n\r\n"), 0o600))
			},
		},
		{
			name: "symlinked lockfile",
			prepare: func(t *testing.T, lockPath string) {
				require.NoError(t, os.Rename(lockPath, lockPath+".real"))
				require.NoError(t, os.Symlink(filepath.Base(lockPath)+".real", lockPath))
			},
		},
		{
			name: "symlinked github directory",
			prepare: func(t *testing.T, _ string) {
				require.NoError(t, os.Rename(".github", "github-real"))
				require.NoError(t, os.Symlink("github-real", ".github"))
			},
		},
		{
			name: "symlinked workflows directory",
			prepare: func(t *testing.T, _ string) {
				require.NoError(t, os.Rename(filepath.Join(".github", "workflows"), filepath.Join(".github", "workflows-real")))
				require.NoError(t, os.Symlink("workflows-real", filepath.Join(".github", "workflows")))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempWorkflow(t, shellOnlyWorkflow)
			require.NoError(t, os.Mkdir(".git", 0o755))
			lockPath := filepath.Join(".github", "workflows", "actions.lock")
			require.NoError(t, os.WriteFile(lockPath, []byte(emptyDependencyLockfile), 0o600))
			tt.prepare(t, lockPath)
			reg := &httpmock.Registry{}
			defer reg.Verify(t)
			_, _, err := runCommandWithHTTP(t, reg, "--verify-local", "--require-lockfile", "--json=valid,findings", path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), ".github")
		})
	}
}

func TestRequireLockfileAcceptsShellOnlyWorkflowWithRegularLockfile(t *testing.T) {
	path := writeTempWorkflow(t, shellOnlyWorkflow)
	require.NoError(t, os.Mkdir(".git", 0o755))
	lockPath := filepath.Join(".github", "workflows", "actions.lock")
	before := []byte(emptyDependencyLockfile)
	require.NoError(t, os.WriteFile(lockPath, before, 0o600))
	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	stdout, _, err := runCommandWithHTTP(t, reg, "--verify-local", "--require-lockfile", "--json=valid,findings", path)
	require.NoError(t, err)
	var payload struct {
		Valid bool `json:"valid"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
	assert.True(t, payload.Valid)
	after, err := os.ReadFile(lockPath)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestVerifyLocalAcceptsShellOnlyWorkflowWithoutLockfileByDefault(t *testing.T) {
	path := writeTempWorkflow(t, shellOnlyWorkflow)
	require.NoError(t, os.Mkdir(".git", 0o755))
	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	stdout, _, err := runCommandWithHTTP(t, reg, "--verify-local", "--json=valid,findings", path)
	require.NoError(t, err)
	var payload struct {
		Valid bool `json:"valid"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
	assert.True(t, payload.Valid)
	_, err = os.Stat(filepath.Join(".github", "workflows", "actions.lock"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestRequireLockfileAcceptsTrustedCheckoutAlias(t *testing.T) {
	path := writeTempWorkflow(t, shellOnlyWorkflow)
	require.NoError(t, os.Mkdir(".git", 0o755))
	require.NoError(t, os.WriteFile(".github/workflows/actions.lock", []byte(emptyDependencyLockfile), 0o600))
	root, err := os.Getwd()
	require.NoError(t, err)
	alias := filepath.Join(t.TempDir(), "checkout")
	require.NoError(t, os.Symlink(root, alias))
	t.Setenv("PWD", alias)
	registry := &httpmock.Registry{}
	defer registry.Verify(t)
	_, _, err = runCommandWithHTTP(t, registry, "--verify-local", "--require-lockfile", path)
	require.NoError(t, err)
}
