package pin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/github/gh-actions-lock/internal/lockfile"
	"github.com/github/gh-actions-lock/internal/pipeline"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommitRejectsUnsupportedRewriteBeforeAnyWrite(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	t.Chdir(dir)
	good := ".github/workflows/a.yml"
	unsupported := ".github/workflows/b.yml"
	contents := map[string][]byte{
		good:        []byte("jobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4\n"),
		unsupported: []byte("jobs:\n  test:\n    steps:\n      - uses: >-\n          actions/checkout@v4\n"),
	}
	for path, content := range contents {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, content, 0o640))
	}
	store, err := lockfile.LoadState(dir, fakeMeta{})
	require.NoError(t, err)
	rec := &Record{}
	for _, path := range []string{good, unsupported} {
		rec.Workflows = append(rec.Workflows, WorkflowPlan{Path: path, Rewrites: map[string]string{
			"actions/checkout@v4": "actions/checkout@v4.2.1",
		}})
		rec.Entries = append(rec.Entries, Entry{
			NWO: "actions/checkout", Ref: "v4.2.1", SHA: strings.Repeat("a", 40),
			Resolution: Pinned, Direct: true, Workflows: []string{path},
		})
	}
	err = Commit(context.Background(), rec, store, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiline")
	for path, before := range contents {
		after, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		assert.Equal(t, before, after)
	}
	_, err = os.Stat(".github/workflows/actions.lock")
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.False(t, rec.Committed)
}

func TestCommitRejectsCyclicCandidateBeforeSourceWrite(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	t.Chdir(dir)
	path := ".github/workflows/ci.yml"
	before := []byte("jobs:\n  test:\n    steps:\n      - uses: owner/a@v1\n")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, before, 0o640))
	require.NoError(t, os.Chmod(path, 0o640))
	store, err := lockfile.LoadState(dir, fakeMeta{})
	require.NoError(t, err)
	rec := &Record{
		Workflows: []WorkflowPlan{{Path: path, Rewrites: map[string]string{"owner/a@v1": "owner/a@v1.0.0"}}},
		Entries: []Entry{
			{NWO: "owner/a", Ref: "v1.0.0", SHA: strings.Repeat("a", 40), Resolution: Pinned, Direct: true, Workflows: []string{path}, RequiredBy: []string{"owner/b@v1"}},
			{NWO: "owner/b", Ref: "v1", SHA: strings.Repeat("b", 40), Resolution: Pinned, Workflows: []string{path}, RequiredBy: []string{"owner/a@v1.0.0"}},
		},
	}
	err = Commit(context.Background(), rec, store, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	_, err = os.Stat(".github/workflows/actions.lock")
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestCommitPreservesModeAndMarksApplied(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	t.Chdir(dir)
	path := ".github/workflows/ci.yml"
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("jobs:\n  test:\n    steps:\n      - {uses: actions/checkout@v4, name: 'label # stable'}\n"), 0o640))
	require.NoError(t, os.Chmod(path, 0o640))
	store, err := lockfile.LoadState(dir, fakeMeta{})
	require.NoError(t, err)
	rec := &Record{
		Workflows: []WorkflowPlan{{Path: path, Rewrites: map[string]string{"actions/checkout@v4": "actions/checkout@v4.2.1"}}},
		Entries:   []Entry{{NWO: "actions/checkout", Ref: "v4.2.1", SHA: strings.Repeat("a", 40), Resolution: Pinned, Direct: true, Workflows: []string{path}}},
	}
	require.NoError(t, Commit(context.Background(), rec, store, nil))
	assert.True(t, rec.Committed)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(after), "uses: actions/checkout@v4.2.1")
	assert.Contains(t, string(after), "name: 'label # stable'")
	loaded, err := lockfile.LoadState(dir, nil)
	require.NoError(t, err)
	deps, err := loaded.Get(path)
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.Equal(t, "v4.2.1", deps[0].Ref)
}

func TestCommitCanceledBeforeSaveRestoresSources(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	t.Chdir(dir)
	path := ".github/workflows/ci.yml"
	before := []byte("jobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4\n")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, before, 0o640))
	require.NoError(t, os.Chmod(path, 0o640))
	store, err := lockfile.LoadState(dir, fakeMeta{})
	require.NoError(t, err)
	rec := &Record{
		Workflows: []WorkflowPlan{{Path: path, Rewrites: map[string]string{"actions/checkout@v4": "actions/checkout@v4.2.1"}}},
		Entries:   []Entry{{NWO: "actions/checkout", Ref: "v4.2.1", SHA: strings.Repeat("a", 40), Resolution: Pinned, Direct: true, Workflows: []string{path}}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = Commit(ctx, rec, store, &CommitOptions{OnProgress: func(phase string) {
		if phase == "Saving lockfile" {
			cancel()
		}
	}})
	assert.ErrorIs(t, err, context.Canceled)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	_, err = os.Stat(".github/workflows/actions.lock")
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.False(t, rec.Committed)
}

func TestCommitRejectsSourceSelectionChanges(t *testing.T) {
	for _, change := range []string{"preferred manifest added", "symlink retargeted", "source edited"} {
		t.Run(change, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			dir := t.TempDir()
			t.Chdir(dir)
			require.NoError(t, os.Mkdir(".git", 0o755))
			path := ".github/workflows/ci.yml"
			workflow := []byte("jobs:\n  test:\n    steps:\n      - uses: $/local\n")
			body := []byte("runs:\n  using: composite\n  steps:\n    - uses: actions/checkout@v4\n")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, workflow, 0o644))
			require.NoError(t, os.Mkdir("local", 0o755))
			source := "local/action.yml"
			switch change {
			case "preferred manifest added":
				source = "local/action.yaml"
			case "symlink retargeted":
				source = "local/first.yml"
				require.NoError(t, os.WriteFile("local/second.yml", body, 0o644))
				require.NoError(t, os.Symlink("first.yml", "local/action.yml"))
			}
			require.NoError(t, os.WriteFile(source, body, 0o644))
			store, err := lockfile.LoadState(dir, fakeMeta{})
			require.NoError(t, err)
			pw := pipeline.ParseAll([]string{path}, store)[0]
			require.Empty(t, pw.SelfRepositoryResolutionErrs)
			rec := &Record{
				Workflows: []WorkflowPlan{{Path: path, SelfActionFiles: pw.SelfActionFiles, SourceContents: pw.SourceContents, SourcePaths: pw.SourcePaths,
					Rewrites: map[string]string{"actions/checkout@v4": "actions/checkout@v4.2.1"}}},
				Entries: []Entry{{NWO: "actions/checkout", Ref: "v4.2.1", SHA: strings.Repeat("a", 40),
					Resolution: Pinned, Direct: true, Workflows: []string{path}}},
			}
			switch change {
			case "preferred manifest added":
				require.NoError(t, os.WriteFile("local/action.yml", []byte("runs:\n  using: node20\n"), 0o644))
			case "symlink retargeted":
				require.NoError(t, os.Remove("local/action.yml"))
				require.NoError(t, os.Symlink("second.yml", "local/action.yml"))
			case "source edited":
				body = []byte("runs:\n  using: composite\n  steps:\n    - uses: actions/setup-go@v5\n")
				require.NoError(t, os.WriteFile(source, body, 0o644))
			}
			err = Commit(context.Background(), rec, store, nil)
			require.ErrorContains(t, err, "since")
			assert.False(t, rec.Committed)
			for name, want := range map[string][]byte{path: workflow, source: body} {
				got, err := os.ReadFile(name)
				require.NoError(t, err)
				assert.Equal(t, want, got)
			}
			_, err = os.Stat(".github/workflows/actions.lock")
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestCommitPreservesWorkflowSymlink(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	t.Chdir(dir)
	path := ".github/workflows/ci.yml"
	target := ".github/workflows/shared.yml"
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(target, []byte("jobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4\n"), 0o644))
	require.NoError(t, os.Symlink("shared.yml", path))
	store, err := lockfile.LoadState(dir, fakeMeta{})
	require.NoError(t, err)
	pw := pipeline.ParseAll([]string{path}, store)[0]
	rec := &Record{
		Workflows: []WorkflowPlan{{Path: path, SourceContents: pw.SourceContents, SourcePaths: pw.SourcePaths,
			Rewrites: map[string]string{"actions/checkout@v4": "actions/checkout@v4.2.1"}}},
		Entries: []Entry{{NWO: "actions/checkout", Ref: "v4.2.1", SHA: strings.Repeat("a", 40),
			Resolution: Pinned, Direct: true, Workflows: []string{path}}},
	}
	require.NoError(t, Commit(context.Background(), rec, store, nil))
	link, err := os.Readlink(path)
	require.NoError(t, err)
	assert.Equal(t, "shared.yml", link)
	updated, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Contains(t, string(updated), "actions/checkout@v4.2.1")
	current := pipeline.ParseAll([]string{path}, store)[0]
	_, missing := current.PartitionRefs()
	assert.Empty(t, missing)
}
