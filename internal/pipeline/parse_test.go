package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/github/gh-actions-lock/internal/lockfile"
	"github.com/github/gh-actions-lock/internal/pinpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAllExpandsSelfRepositoryActionDependencies(t *testing.T) {
	repoRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755))

	workflowPath := filepath.Join(repoRoot, ".github", "workflows", "ci.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	require.NoError(t, os.WriteFile(workflowPath, []byte(`on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: $/actions/root
  call:
    uses: $/.github/workflows/reusable.yml
`), 0o644))

	actionPath := filepath.Join(repoRoot, "actions", "root", "action.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(actionPath), 0o755))
	require.NoError(t, os.WriteFile(actionPath, []byte(`runs:
  using: composite
  steps:
    - uses: actions/checkout@v4
`), 0o644))
	reusablePath := filepath.Join(repoRoot, ".github", "workflows", "reusable.yml")
	require.NoError(t, os.WriteFile(reusablePath, []byte("jobs:\n  build:\n    steps:\n      - run: true\n"), 0o644))

	parsed := ParseAll([]string{workflowPath}, nil)

	require.Len(t, parsed, 1)
	pw := parsed[0]
	require.Len(t, pw.Refs, 1)
	assert.Equal(t, "actions/checkout", pw.Refs[0].NWO())
	assert.Equal(t, "v4", pw.Refs[0].Ref)
	assert.NotEmpty(t, pw.RewriteRefs, "refs inside a self repository action are rewritable in their action file")
	assert.Equal(t, []string{actionPath, reusablePath}, pw.SelfActionFiles)
	assert.ElementsMatch(t, []string{"$/actions/root", "$/.github/workflows/reusable.yml"}, pw.SelfRepositoryRefs)
	assert.Empty(t, pw.SelfRepositoryResolutionErrs)
}

func TestParseAllIncludesCalledLocalWorkflowSources(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
	files := map[string]string{
		".github/workflows/ci.yml": "jobs: {a: {uses: ./.github/workflows/first.yaml}}\n",
		".github/workflows/first.yaml": `jobs:
  local:
    uses: $/.github/workflows/second.yml
  remote:
    uses: vendor/flows/.github/workflows/build.yml@v2
`,
		".github/workflows/second.yml": "jobs: {a: {steps: [{uses: $/actions/local}, {uses: vendor/leaf@v1}]}}\n",
		"actions/local/action.yml":     "runs: {using: composite, steps: [{uses: vendor/nested@v3}]}\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	caller := filepath.Join(root, ".github", "workflows", "ci.yml")
	parsed := ParseAll([]string{caller}, nil)
	require.Len(t, parsed, 1)
	pw := parsed[0]
	require.Len(t, pw.Refs, 3)
	assert.Equal(t, ".github/workflows/build.yml", pw.Refs[0].Path)
	assert.Equal(t, "vendor/leaf", pw.Refs[1].NWO())
	assert.Equal(t, "vendor/nested", pw.Refs[2].NWO())
	assert.Equal(t, pw.Refs, pw.RewriteRefs)
	assert.Equal(t, []string{
		filepath.Join(root, ".github", "workflows", "first.yaml"),
		filepath.Join(root, ".github", "workflows", "second.yml"),
		filepath.Join(root, "actions", "local", "action.yml"),
	}, pw.SelfActionFiles)
	assert.Empty(t, pw.SelfRepositoryResolutionErrs)
	for name, content := range files {
		assert.Equal(t, []byte(content), pw.SourceContents[filepath.Join(root, filepath.FromSlash(name))], "retain parsed source %s", name)
	}
}

func TestParseAllCapturesSourceSymlinkIdentities(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
	files := map[string]string{
		"definitions/primary.yml": "jobs: {a: {uses: ./.github/workflows/called.yml}}\n",
		"definitions/called.yml":  "jobs: {a: {steps: [{uses: $/actions/local}]}}\n",
		"definitions/action.yml":  "runs: {using: composite, steps: [{uses: vendor/action@v1}]}\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	aliases := map[string]string{
		".github/workflows/ci.yml":     "definitions/primary.yml",
		".github/workflows/called.yml": "definitions/called.yml",
		"actions/local/action.yaml":    "definitions/action.yml",
	}
	for alias, target := range aliases {
		path := filepath.Join(root, filepath.FromSlash(alias))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		if err := os.Symlink(filepath.Join(root, filepath.FromSlash(target)), path); err != nil {
			t.Skipf("creating source symlink: %v", err)
		}
	}
	primary := filepath.Join(root, ".github", "workflows", "ci.yml")
	parsed := ParseAll([]string{primary}, nil)
	require.Len(t, parsed, 1)
	pw := parsed[0]
	require.NoError(t, pw.LoadErr)
	require.Empty(t, pw.SelfRepositoryResolutionErrs)
	for alias, target := range aliases {
		path := filepath.Join(root, filepath.FromSlash(alias))
		identity := filepath.Join(root, filepath.FromSlash(target))
		assert.Equal(t, identity, pw.SourcePaths[path], "retain the identity read through %s", alias)
		assert.Equal(t, []byte(files[target]), pw.SourceContents[path])
	}
	for _, target := range []string{"definitions/called.yml", "definitions/action.yml"} {
		identity := filepath.Join(root, filepath.FromSlash(target))
		assert.Equal(t, identity, pw.SourcePaths[identity])
	}
	store, err := lockfile.LoadState(root, noopMeta{})
	require.NoError(t, err)
	report := DiagnoseParsed(context.Background(), parsed, nil, store, pinpool.New(1, nil))
	require.Len(t, report.Workflows, 1)
	assert.Equal(t, pw.SourcePaths, report.Workflows[0].SourcePaths)
}
