package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
	"github.com/github/gh-actions-lock/cmd/gh-actions-lock/format"
	"github.com/github/gh-actions-lock/internal/ghapi/httpmock"
	"github.com/github/gh-actions-lock/internal/pipeline/checks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyLocalRequiresCoverageForBareSHA(t *testing.T) {
	path := writeTempWorkflow(t, `
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	stdout, _, err := runCommandWithHTTP(t, reg, "--verify-local", "--no-fix", "--json=valid,findings", path)
	require.ErrorIs(t, err, errSilent)
	var payload struct {
		Valid    bool             `json:"valid"`
		Findings []format.Finding `json:"findings"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
	assert.False(t, payload.Valid)
	missing, advisory := false, false
	for _, finding := range payload.Findings {
		switch finding.Category {
		case string(checks.NotPinned):
			missing = true
			assert.Equal(t, string(checks.SeverityError), finding.Severity)
		case string(checks.ShaAsRef):
			advisory = true
			assert.Equal(t, string(checks.SeverityWarning), finding.Severity)
		}
	}
	assert.True(t, missing)
	assert.True(t, advisory)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	_, err = os.Stat(".github/workflows/actions.lock")
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestNoOnboardRefusesUnrecordedBareSHA(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	path := writeTempWorkflow(t, `
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@`+sha)
	require.NoError(t, os.Mkdir(".git", 0o755))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(httpmock.GraphQLForRepo("actions", "checkout"), httpmock.JSONResponse(map[string]any{
		"data": map[string]any{"a0": testRepoResponse("actions/checkout", sha, nodeActionYAML)},
	}))
	stdout, _, err := runCommandWithHTTP(t, reg, "--no-onboard", "--json=valid,findings", path)
	require.NoError(t, err)
	var payload struct {
		Findings []format.Finding `json:"findings"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
	refused := false
	for _, finding := range payload.Findings {
		if finding.Category == string(checks.OnboardingRequired) {
			refused = true
		}
	}
	assert.True(t, refused)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	_, err = os.Stat(".github/workflows/actions.lock")
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestNoOnboardDropsRefusedSHARewrite(t *testing.T) {
	ref := parserlock.ActionRef{Owner: "actions", Repo: "checkout", Ref: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	pw := checks.ParsedWorkflow{Path: ".github/workflows/ci.yml", Refs: []parserlock.ActionRef{ref}}
	report := &checks.Report{Workflows: []checks.WorkflowReport{{
		Path: pw.Path, ActionRefs: pw.Refs, RewriteRefs: pw.Refs,
		Findings: checks.RunChecks(context.Background(), pw, parserlock.File{}, nil),
	}}}
	assert.Len(t, gateNoOnboard(report, nil), 1)
	assert.Empty(t, report.Workflows[0].ActionRefs)
	assert.Empty(t, report.Workflows[0].RewriteRefs)
	assert.Equal(t, 1, report.Workflows[0].CountByCategory(checks.OnboardingRequired))
}

func TestNoOnboardRejectsNewBareSHAInRecordedWorkflow(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	const checkoutSHA = "de0fac2e4500dabe0009e67214ff5f5447ce83dd"
	const setupGoSHA = "4a3601121dd01d1626a1e23e37211e3254c1c06c"
	path := writeTempWorkflow(t, "jobs:\n  test:\n    steps:\n      - uses: actions/checkout@v6\n      - uses: actions/setup-go@"+setupGoSHA+"\n")
	require.NoError(t, os.Mkdir(".git", 0o755))
	workflowBefore, err := os.ReadFile(path)
	require.NoError(t, err)
	lockPath := filepath.Join(".github", "workflows", "actions.lock")
	lockBefore := []byte("version: '" + parserlock.Version + "'\ndependencies:\n" +
		"  'actions/checkout@v6':\n    ref: 'v6'\n    commit: 'sha1-" + checkoutSHA + "'\n    owner_id: 1\n    repo_id: 1\n" +
		"workflows:\n  '" + filepath.ToSlash(path) + "':\n    - 'actions/checkout@v6'\n")
	require.NoError(t, os.WriteFile(lockPath, lockBefore, 0o600))
	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	registerParity(reg)
	reg.Register(httpmock.GraphQLForRepo("actions", "setup-go"), httpmock.JSONResponse(map[string]any{
		"data": map[string]any{"a0": testRepoResponse("actions/setup-go", setupGoSHA, nodeActionYAML)},
	}))
	stdout, _, err := runCommandWithHTTP(t, reg, "--no-onboard", "--no-narrow", "--json=valid,findings,pins", path)
	require.ErrorIs(t, err, errSilent)
	var payload struct {
		Valid    bool             `json:"valid"`
		Findings []format.Finding `json:"findings"`
		Pins     []format.Pin     `json:"pins"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
	assert.False(t, payload.Valid)
	found := false
	for _, finding := range payload.Findings {
		if finding.Category == string(checks.OnboardingRequired) {
			found = true
			assert.Equal(t, string(checks.SeverityError), finding.Severity)
		}
	}
	assert.True(t, found)
	for _, outcome := range payload.Pins {
		assert.False(t, outcome.Applied)
	}
	for name, want := range map[string][]byte{path: workflowBefore, lockPath: lockBefore} {
		got, err := os.ReadFile(name)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestFreshLocalActionsBlockWrites(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := writeTempWorkflow(t, `
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: ./generated/action
`)
	require.NoError(t, os.Mkdir(".git", 0o755))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	stdout, _, err := runCommandWithHTTP(t, reg, "--json=valid,findings", path)
	require.ErrorIs(t, err, errSilent)
	var payload struct {
		Valid    bool             `json:"valid"`
		Findings []format.Finding `json:"findings"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
	assert.False(t, payload.Valid)
	require.Len(t, payload.Findings, 1)
	assert.Equal(t, string(checks.LocalAction), payload.Findings[0].Category)
	assert.Equal(t, string(checks.SeverityError), payload.Findings[0].Severity)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	_, err = os.Stat(".github/workflows/actions.lock")
	assert.ErrorIs(t, err, os.ErrNotExist)
}
