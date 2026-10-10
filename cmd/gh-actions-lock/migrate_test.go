package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/github/gh-actions-lock/internal/ghapi/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrateLocalActions_CompositeActionFiles verifies that migration
// rewrites `./…` not just in workflow files but also inside in-repo composite
// action definitions (action.yml), including ones nested a few directories deep.
// A `./…` whose target does not resolve to an in-repo action file is left
// untouched because its source cannot be inspected.
func TestMigrateLocalActions_CompositeActionFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		full := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}

	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
	write(".github/workflows/ci.yml",
		"name: CI\non: push\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: ./my-action\n")
	write("my-action/action.yml",
		"name: My Action\nruns:\n  using: composite\n  steps:\n    - uses: ./helper\n    - uses: ./nonexistent\n")
	write("helper/action.yml",
		"name: Helper\nruns:\n  using: composite\n  steps:\n    - uses: ./nested/deep\n")
	write("nested/deep/action.yml",
		"name: Deep\nruns:\n  using: composite\n  steps:\n    - run: echo deep\n      shell: bash\n")

	t.Chdir(dir)

	total, _, err := migrateLocalActions([]string{filepath.Join(".github", "workflows", "ci.yml")})
	require.NoError(t, err)

	// workflow ./my-action, composite ./helper, composite ./nested/deep = 3.
	assert.Equal(t, 3, total)

	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		require.NoError(t, err)
		return string(b)
	}
	assert.Contains(t, read(".github/workflows/ci.yml"), "uses: $/my-action")
	assert.Contains(t, read("my-action/action.yml"), "uses: $/helper")
	assert.Contains(t, read("helper/action.yml"), "uses: $/nested/deep")
	// Unresolvable local path stays put — nothing at repo-root/nonexistent.
	assert.Contains(t, read("my-action/action.yml"), "uses: ./nonexistent")
}

func TestMigrateLocalActions_StructuralErrorPreventsAllWrites(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) string {
		full := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
		return full
	}
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
	write("local/action.yml", "runs:\n  using: composite\n")
	firstPath := write(".github/workflows/first.yml",
		"jobs:\n  build:\n    steps:\n      - uses: ./local\n")
	secondPath := write(".github/workflows/second.yml",
		"jobs:\n  build:\n    steps:\n      - uses: $/bad@v1\n")
	firstBefore, err := os.ReadFile(firstPath)
	require.NoError(t, err)

	total, _, err := migrateLocalActions([]string{firstPath, secondPath})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid self repository reference")
	assert.Zero(t, total)
	firstAfter, readErr := os.ReadFile(firstPath)
	require.NoError(t, readErr)
	assert.Equal(t, firstBefore, firstAfter)
}

func TestMigrateLocalActions_RejectsActionFileSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
	workflowPath := filepath.Join(dir, ".github", "workflows", "ci.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	workflow := []byte("jobs:\n  build:\n    steps:\n      - uses: ./escape\n")
	require.NoError(t, os.WriteFile(workflowPath, workflow, 0o644))

	outsidePath := filepath.Join(t.TempDir(), "action.yml")
	outside := []byte("runs:\n  using: composite\n  steps:\n    - uses: ./helper\n")
	require.NoError(t, os.WriteFile(outsidePath, outside, 0o644))
	escapeDir := filepath.Join(dir, "escape")
	require.NoError(t, os.MkdirAll(escapeDir, 0o755))
	if err := os.Symlink(outsidePath, filepath.Join(escapeDir, "action.yml")); err != nil {
		t.Skipf("creating symlink: %v", err)
	}

	total, _, err := migrateLocalActions([]string{workflowPath})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside repository root")
	assert.Zero(t, total)
	gotWorkflow, readErr := os.ReadFile(workflowPath)
	require.NoError(t, readErr)
	assert.Equal(t, workflow, gotWorkflow)
	gotOutside, readErr := os.ReadFile(outsidePath)
	require.NoError(t, readErr)
	assert.Equal(t, outside, gotOutside)
}

func TestMigrateLocalActions_LeavesUnrelatedFilesUnchanged(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.Mkdir(".git", 0o755))
	files := map[string]string{
		".github/workflows/selected.yml": "jobs:\n  build:\n    steps:\n      - uses: ./selected\n",
		"selected/action.yml":            "runs:\n  using: composite\n  steps:\n    - uses: ./helper\n",
		"helper/action.yml":              "runs:\n  using: composite\n  steps:\n    - run: echo helper\n      shell: bash\n",
		".github/workflows/other.yml":    "jobs:\n  build:\n    steps:\n      - uses: ./other\n",
		"other/action.yml":               "runs:\n  using: composite\n  steps:\n    - uses: ./helper\n",
		"unreferenced/action.yml":        "runs:\n  using: composite\n  steps:\n    - uses: $/bad@v1\n",
	}
	for path, content := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}

	count, _, err := migrateLocalActions([]string{".github/workflows/selected.yml"})

	require.NoError(t, err, "an unrelated invalid action must not enter the selected migration scope")
	assert.Equal(t, 2, count)
	for _, path := range []string{".github/workflows/other.yml", "other/action.yml", "unreferenced/action.yml"} {
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, files[path], string(got), path)
	}
}

func TestVerifyLocalWithLocalActionsPreservesFiles(t *testing.T) {
	for name, flags := range map[string][]string{
		"default":                    nil,
		"explicit migration ignored": {"--migrate-local-actions"},
	} {
		t.Run(name, func(t *testing.T) {
			workflow := writeTempWorkflow(t, "jobs:\n  build:\n    steps:\n      - uses: ./.github/actions/local\n")
			require.NoError(t, os.Mkdir(".git", 0o755))
			action := filepath.Join(".github", "actions", "local", "action.yml")
			require.NoError(t, os.MkdirAll(filepath.Dir(action), 0o755))
			require.NoError(t, os.WriteFile(action, []byte("runs:\n  using: composite\n  steps:\n    - uses: ./helper\n"), 0o600))
			lock := filepath.Join(".github", "workflows", "actions.lock")
			require.NoError(t, os.WriteFile(lock, []byte("version: 'v0.0.3'\nworkflows:\n  '.github/workflows/workflow.yml': []\ndependencies: {}\n"), 0o600))
			before := snapshotWorkflows(t)
			transport := &requestCountingTransport{}
			args := append([]string{"--verify-local", "--json=valid,findings"}, flags...)
			stdout, _, err := runCommandWithHTTP(t, transport, append(args, workflow)...)

			require.ErrorIs(t, err, errSilent)
			assert.Equal(t, before, snapshotWorkflows(t))
			assert.Zero(t, transport.calls.Load(), "offline verification must not contact GitHub")
			var payload struct {
				Valid    bool `json:"valid"`
				Findings []struct {
					Category string `json:"category"`
					Severity string `json:"severity"`
				} `json:"findings"`
			}
			require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
			assert.False(t, payload.Valid)
			require.Len(t, payload.Findings, 1)
			assert.Equal(t, "local-action", payload.Findings[0].Category)
			assert.Equal(t, "error", payload.Findings[0].Severity)
		})
	}
}

func TestLocalActionsDoNotSkipRemotePinVerification(t *testing.T) {
	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(httpmock.GraphQL(`commit: object\(oid`), repoIDMismatch{}.RoundTrip)
	workflow := writeTempWorkflow(t, `
jobs:
  build:
    steps:
      - uses: ./generated/action
      - uses: actions/checkout@v4.2.1
`, "actions/checkout@v4.2.1=sha1-"+parityTestSHA)
	before := snapshotWorkflows(t)

	stdout, _, err := runCommandWithHTTP(t, reg, "--no-fix", "--json=valid,findings", workflow)

	require.ErrorIs(t, err, errSilent)
	assert.Equal(t, before, snapshotWorkflows(t))
	var payload struct {
		Valid    bool `json:"valid"`
		Findings []struct {
			Category string `json:"category"`
			Severity string `json:"severity"`
			Detail   string `json:"detail"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
	assert.False(t, payload.Valid)
	local, mismatch := false, false
	for _, finding := range payload.Findings {
		if finding.Category == "local-action" {
			local = true
			assert.Equal(t, "error", finding.Severity)
		}
		if strings.Contains(finding.Detail, "different repository than the one locked") {
			mismatch = true
			assert.Equal(t, "error", finding.Severity)
		}
	}
	assert.True(t, local, "workspace-relative actions must report incomplete coverage")
	assert.True(t, mismatch, "known remote pins must still undergo repository identity checks")
}

// TestMigrateLocalActions_EndToEnd runs an explicitly opted-in migration
// against a scratch repo whose local composite chain
// reaches a pinnable remote action, then asserts on BOTH sides of
// the mutation: the rewritten workflow/action files AND the generated
// lockfile. It proves the two behaviours compose correctly in one pass:
//   - `./…` same-repo refs are rewritten to `$/…` in the workflow and in the
//     in-repo composite action.yml files (including nested ones);
//   - `$/…` self refs are inherently pinned, so they never appear in the
//     lockfile, while the genuine remote dep (actions/setup-go) is pinned to
//     its resolved commit.
func TestMigrateLocalActions_EndToEnd(t *testing.T) {
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	setupGoSHA := "4a3601121dd01d1626a1e23e37211e3254c1c06c"

	reg.Register(
		httpmock.GraphQLForRepo("actions", "setup-go"),
		httpmock.JSONResponse(map[string]any{
			"data": map[string]any{
				"a0": testRepoResponse("actions/setup-go", setupGoSHA, nodeActionYAML),
			},
		}),
	)
	reg.Register(
		httpmock.REST("GET", `repos/actions/setup-go/tags`),
		httpmock.JSONResponse([]map[string]any{
			{"name": "v6.0.0", "commit": map[string]any{"sha": setupGoSHA}},
		}),
	)
	reg.Register(httpmock.REST("GET", `repos/actions/setup-go$`), httpmock.JSONResponse(map[string]any{
		"default_branch": "main",
		"id":             2,
		"owner":          map[string]any{"id": 1},
	}))

	dir := t.TempDir()
	write := func(rel, content string) {
		full := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o600))
	}
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))

	// The workflow only names the local composite. The remote dependency lives
	// three levels down the in-repo action chain.
	write(".github/workflows/workflow.yml", strings.TrimSpace(`
name: ci
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: ./my-action
`)+"\n")
	// In-repo composite chain, all same-repo local refs.
	write("my-action/action.yml", "name: My Action\nruns:\n  using: composite\n  steps:\n    - uses: ./helper\n")
	write("helper/action.yml", "name: Helper\nruns:\n  using: composite\n  steps:\n    - uses: ./nested/deep\n")
	write("nested/deep/action.yml", "name: Deep\nruns:\n  using: composite\n  steps:\n    - uses: actions/setup-go@v6\n")

	t.Chdir(dir)

	_, _, err := runCommandWithHTTP(t, reg, "--migrate-local-actions")
	require.NoError(t, err)

	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		require.NoError(t, err)
		return string(b)
	}

	// Workflow: local ref migrated; the remote ref remains in the action file.
	wf := read(".github/workflows/workflow.yml")
	assert.Contains(t, wf, "uses: $/my-action")
	assert.NotContains(t, wf, "uses: ./my-action")
	assert.NotContains(t, wf, "actions/setup-go")

	// In-repo composite action files migrated, including the nested one.
	assert.Contains(t, read("my-action/action.yml"), "uses: $/helper")
	assert.Contains(t, read("helper/action.yml"), "uses: $/nested/deep")
	assert.Contains(t, read("nested/deep/action.yml"), "uses: actions/setup-go@v6.0.0",
		"remote refs inside $/ actions are narrowed in their action file")

	// Lockfile: pins the remote dep, records nothing for the self refs.
	lock := readTempLockfilePins(t)
	assert.Contains(t, lock, "actions/setup-go", "remote dep should be pinned")
	assert.Contains(t, lock, "'actions/setup-go@v6.0.0':", "narrowed ref matches the rewritten action file")
	assert.Contains(t, lock, setupGoSHA)
	assert.NotContains(t, lock, "$/", "self refs are inherently pinned; no lockfile entry")
	assert.NotContains(t, lock, "my-action", "in-repo composite must not be pinned")
	assert.NotContains(t, lock, "helper")
}

func TestMigrateLocalActions_RequiresOptIn(t *testing.T) {
	const workflow = `name: CI
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: ./.github/actions/foo
`
	setup := func(t *testing.T) string {
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
		wf := filepath.Join(dir, ".github", "workflows", "ci.yml")
		require.NoError(t, os.MkdirAll(filepath.Dir(wf), 0o755))
		require.NoError(t, os.WriteFile(wf, []byte(workflow), 0o600))
		act := filepath.Join(dir, ".github", "actions", "foo", "action.yml")
		require.NoError(t, os.MkdirAll(filepath.Dir(act), 0o755))
		require.NoError(t, os.WriteFile(act,
			[]byte("name: Foo\nruns:\n  using: composite\n  steps:\n    - run: echo hi\n      shell: bash\n"), 0o600))
		t.Chdir(dir)
		return wf
	}
	read := func(t *testing.T, path string) string {
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		return string(b)
	}

	t.Run("explicit migration rewrites to $/", func(t *testing.T) {
		transport := &requestCountingTransport{}
		wf := setup(t)
		_, _, err := runCommandWithHTTP(t, transport, "--migrate-local-actions", wf)
		require.NoError(t, err)
		got := read(t, wf)
		assert.Contains(t, got, "uses: $/.github/actions/foo")
		assert.NotContains(t, got, "uses: ./.github/actions/foo")
		assert.Zero(t, transport.calls.Load(), "self ref is inherently pinned; no network")
	})

	t.Run("default reports incomplete coverage and preserves source", func(t *testing.T) {
		transport := &requestCountingTransport{}
		wf := setup(t)
		_, stderr, err := runCommandWithHTTP(t, transport, wf)
		require.ErrorIs(t, err, errSilent)
		assert.Contains(t, stderr, "dependency coverage is incomplete")
		assert.Equal(t, workflow, read(t, wf))
		assert.NoFileExists(t, filepath.Join(".github", "workflows", "actions.lock"))
		assert.Zero(t, transport.calls.Load())
	})

	t.Run("--no-fix reports incomplete coverage without writing", func(t *testing.T) {
		transport := &requestCountingTransport{}
		wf := setup(t)
		_, _, err := runCommandWithHTTP(t, transport, "--no-fix", "--migrate-local-actions", wf)
		require.ErrorIs(t, err, errSilent)
		assert.Equal(t, workflow, read(t, wf))
		assert.NoFileExists(t, filepath.Join(".github", "workflows", "actions.lock"))
	})

	t.Run("--no-migrate-local-actions suppresses rewrite", func(t *testing.T) {
		transport := &requestCountingTransport{}
		wf := setup(t)
		_, _, err := runCommandWithHTTP(t, transport, "--no-migrate-local-actions", wf)
		require.ErrorIs(t, err, errSilent)
		got := read(t, wf)
		assert.Contains(t, got, "uses: ./.github/actions/foo", "opt-out leaves the ./… ref in place")
		assert.NotContains(t, got, "$/", "opt-out means no migration to $/")
	})
}

func TestNoMigrateLocalActions_PreservesExistingPins(t *testing.T) {
	const workflowWithRemoteAction = `name: CI
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: ./.github/actions/foo
      - uses: actions/checkout@v4
`
	const workflowWithOnlyLocalAction = `name: CI
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: ./.github/actions/foo
`
	const lock = `version: 'v0.0.2'
dependencies:
  'actions/checkout@v4':
    ref: 'v4'
    commit: 'sha1-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
    owner_id: 1
    repo_id: 1
workflows:
  '.github/workflows/ci.yml':
    - 'actions/checkout@v4'
`

	for name, workflow := range map[string]string{
		"remote and local actions": workflowWithRemoteAction,
		"only local action":        workflowWithOnlyLocalAction,
		"malformed workflow":       "name: [",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			workflowPath := filepath.Join(".github", "workflows", "ci.yml")
			require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
			require.NoError(t, os.WriteFile(workflowPath, []byte(workflow), 0o600))
			lockPath := filepath.Join(".github", "workflows", "actions.lock")
			require.NoError(t, os.WriteFile(lockPath, []byte(lock), 0o600))

			transport := &requestCountingTransport{}
			_, _, err := runCommandWithHTTP(t, transport, "--relock", "--no-migrate-local-actions")
			require.Error(t, err)
			got, readErr := os.ReadFile(lockPath)
			require.NoError(t, readErr)
			assert.Equal(t, 2, strings.Count(string(got), "'actions/checkout@v4'"))
			assert.NotContains(t, string(got), "'.github/workflows/ci.yml': []")
		})
	}
}
