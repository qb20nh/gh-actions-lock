package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
	"github.com/github/gh-actions-lock/internal/dep"
	"github.com/github/gh-actions-lock/internal/lockfile"
	"github.com/github/gh-actions-lock/internal/pipeline/checks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testLockfileContent = `version: 'v0.0.2'
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

func writeTestLockfile(t *testing.T, dir, content string) *lockfile.State {
	t.Helper()
	lockDir := filepath.Join(dir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(lockDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(lockDir, "actions.lock"), []byte(content), 0o644))
	store, err := lockfile.LoadState(dir, nil)
	require.NoError(t, err)
	return store
}

func TestVerifyLocalCoverage_AllPinned(t *testing.T) {
	dir := t.TempDir()
	store := writeTestLockfile(t, dir, testLockfileContent)

	parsed := []checks.ParsedWorkflow{
		{
			Path: ".github/workflows/ci.yml",
			Refs: []parserlock.ActionRef{
				{Owner: "actions", Repo: "checkout", Ref: "v4"},
			},
			ExistingDeps: []dep.Dependency{
				{NWO: "actions/checkout", Ref: "v4", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			},
		},
	}

	report := VerifyLocalCoverage(parsed, store)
	assert.True(t, report.IsValid(), "all pinned should be valid")
	assert.Empty(t, report.Workflows[0].Findings)
}

func TestVerifyLocalCoverage_SelfRepositoryOnlyIsValid(t *testing.T) {
	dir := t.TempDir()
	store := writeTestLockfile(t, dir, testLockfileContent)

	parsed := []checks.ParsedWorkflow{
		{
			Path:               ".github/workflows/self.yml",
			SelfRepositoryRefs: []string{"$/actions/foo"},
		},
	}

	report := VerifyLocalCoverage(parsed, store)
	assert.True(t, report.IsValid(), "self repository refs are inherently pinned")
	assert.Empty(t, report.Workflows[0].Findings)
}

func TestVerifyLocalCoverage_InvalidSelfRepositoryRef(t *testing.T) {
	dir := t.TempDir()
	store := writeTestLockfile(t, dir, testLockfileContent)

	parsed := []checks.ParsedWorkflow{
		{
			Path:                  ".github/workflows/self.yml",
			SelfRepositoryRefErrs: []string{"$/actions/foo@v1"},
		},
	}

	report := VerifyLocalCoverage(parsed, store)
	assert.False(t, report.IsValid(), "`$/…@ref` must fail offline verification")
	require.Len(t, report.Workflows[0].Findings, 1)
	assert.Equal(t, checks.InvalidSelfRepositoryRef, report.Workflows[0].Findings[0].Category)
	assert.Equal(t, checks.SeverityError, report.Workflows[0].Findings[0].Severity)
}

func TestVerifyLocalCoverage_InvalidSelfRepositoryRefWithPinnedRemote(t *testing.T) {
	dir := t.TempDir()
	store := writeTestLockfile(t, dir, testLockfileContent)

	parsed := []checks.ParsedWorkflow{
		{
			Path: ".github/workflows/ci.yml",
			Refs: []parserlock.ActionRef{
				{Owner: "actions", Repo: "checkout", Ref: "v4"},
			},
			SelfRepositoryRefErrs: []string{"$/actions/foo@v1"},
			ExistingDeps: []dep.Dependency{
				{NWO: "actions/checkout", Ref: "v4", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			},
		},
	}

	report := VerifyLocalCoverage(parsed, store)

	assert.False(t, report.IsValid())
	require.Len(t, report.Workflows[0].Findings, 1)
	assert.Equal(t, checks.InvalidSelfRepositoryRef, report.Workflows[0].Findings[0].Category)
}

func TestVerifyLocalCoverage_SelfRepositoryResolutionError(t *testing.T) {
	dir := t.TempDir()
	store := writeTestLockfile(t, dir, testLockfileContent)

	parsed := []checks.ParsedWorkflow{{
		Path:                         ".github/workflows/self.yml",
		SelfRepositoryResolutionErrs: []string{"can't inspect self repository action $/missing"},
	}}

	report := VerifyLocalCoverage(parsed, store)

	assert.False(t, report.IsValid())
	require.Len(t, report.Workflows[0].Findings, 1)
	assert.Equal(t, checks.InvalidSelfRepositoryRef, report.Workflows[0].Findings[0].Category)
}

func TestVerifyLocalCoverage_MissingPin(t *testing.T) {
	dir := t.TempDir()
	store := writeTestLockfile(t, dir, testLockfileContent)

	parsed := []checks.ParsedWorkflow{
		{
			Path: ".github/workflows/ci.yml",
			Refs: []parserlock.ActionRef{
				{Owner: "actions", Repo: "checkout", Ref: "v4"},
				{Owner: "actions", Repo: "setup-go", Ref: "v5"},
			},
			ExistingDeps: []dep.Dependency{
				{NWO: "actions/checkout", Ref: "v4", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			},
		},
	}

	report := VerifyLocalCoverage(parsed, store)
	assert.False(t, report.IsValid(), "missing pin should fail")

	var notPinned []checks.Finding
	for _, f := range report.Workflows[0].Findings {
		if f.Category == checks.NotPinned {
			notPinned = append(notPinned, f)
		}
	}
	assert.Len(t, notPinned, 1)
	assert.Contains(t, notPinned[0].Detail, "setup-go")
}

func TestVerifyLocalCoverage_EmptyWorkflow(t *testing.T) {
	dir := t.TempDir()
	store := writeTestLockfile(t, dir, testLockfileContent)

	parsed := []checks.ParsedWorkflow{
		{
			Path: ".github/workflows/empty.yml",
			Refs: nil,
		},
	}

	report := VerifyLocalCoverage(parsed, store)
	assert.True(t, report.IsValid())
	assert.Empty(t, report.Workflows[0].Findings)
}

func TestVerifyLocalCoverage_LoadError(t *testing.T) {
	dir := t.TempDir()
	store := writeTestLockfile(t, dir, testLockfileContent)

	parsed := []checks.ParsedWorkflow{
		{
			Path:    ".github/workflows/bad.yml",
			LoadErr: assert.AnError,
		},
	}

	report := VerifyLocalCoverage(parsed, store)
	assert.False(t, report.IsValid())
	assert.Equal(t, checks.NotPinned, report.Workflows[0].Findings[0].Category)
	assert.False(t, report.Workflows[0].Findings[0].IsRemediableNotPinned())
}

func TestVerifyLocalCoverage_DepsError(t *testing.T) {
	dir := t.TempDir()
	store := writeTestLockfile(t, dir, testLockfileContent)

	parsed := []checks.ParsedWorkflow{
		{
			Path:    ".github/workflows/ci.yml",
			DepsErr: assert.AnError,
		},
	}

	report := VerifyLocalCoverage(parsed, store)
	assert.False(t, report.IsValid())
	assert.Equal(t, checks.NotPinned, report.Workflows[0].Findings[0].Category)
	assert.True(t, report.Workflows[0].Findings[0].IsRemediableNotPinned())
	assert.Contains(t, report.Workflows[0].Findings[0].Detail, "failed to read dependencies")
}

func TestVerifyLocalCoverage_LocalActionsAreIncomplete(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refs    []parserlock.ActionRef
		missing bool
	}{
		{name: "local action only"},
		{name: "local and covered remote action", refs: []parserlock.ActionRef{{Owner: "actions", Repo: "checkout", Ref: "v4"}}},
		{name: "local and uncovered remote action", refs: []parserlock.ActionRef{{Owner: "actions", Repo: "setup-go", Ref: "v5"}}, missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := writeTestLockfile(t, t.TempDir(), testLockfileContent)
			report := VerifyLocalCoverage([]checks.ParsedWorkflow{{
				Path: ".github/workflows/ci.yml", Refs: tc.refs, LocalPaths: []string{"./generated/action"},
			}}, store)
			assert.False(t, report.IsValid())
			assert.Equal(t, 1, report.Workflows[0].CountByCategory(checks.LocalAction))
			if tc.missing {
				assert.Equal(t, 1, report.Workflows[0].CountByCategory(checks.NotPinned))
			}
		})
	}
}

func TestVerifyLocalCoverage_MissingDependencyMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(parserlock.File)
	}{
		{
			name: "missing direct metadata",
			mutate: func(lf parserlock.File) {
				delete(lf.Dependencies, "actions/checkout@v4")
			},
		},
		{
			name: "missing child metadata",
			mutate: func(lf parserlock.File) {
				action := lf.Dependencies["actions/checkout@v4"]
				action.Uses = []string{"actions/setup-go@v5"}
				lf.Dependencies["actions/checkout@v4"] = action
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := writeTestLockfile(t, t.TempDir(), testLockfileContent)
			// LoadState rejects malformed files; exercise the verifier's own
			// invariant if an in-memory graph becomes incomplete afterward.
			tc.mutate(store.File())
			report := VerifyLocalCoverage([]checks.ParsedWorkflow{{
				Path: ".github/workflows/ci.yml",
				Refs: []parserlock.ActionRef{{Owner: "actions", Repo: "checkout", Ref: "v4"}},
			}}, store)
			assert.False(t, report.IsValid())
			assert.Equal(t, 1, report.Workflows[0].CountByCategory(checks.NotPinned))
		})
	}
}
