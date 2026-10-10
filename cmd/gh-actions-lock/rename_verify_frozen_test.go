package main

import (
	"fmt"
	"os"
	"testing"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
	"github.com/github/gh-actions-lock/internal/ghapi/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParityRecordedTransitiveRenameBlocksAllModes(t *testing.T) {
	for _, mode := range []string{"--verify", "--verify-frozen", "default"} {
		t.Run(mode, func(t *testing.T) {
			path := writeTempWorkflow(t, "jobs: {test: {runs-on: ubuntu-latest, steps: [{uses: acme/comp@"+parityTestMoved+"}]}}\n")
			require.NoError(t, os.Mkdir(".git", 0o755))
			lock := fmt.Sprintf(`version: '%s'
dependencies:
  'acme/comp@%s':
    ref: '%s'
    commit: 'sha1-%s'
    owner_id: 1
    repo_id: 1
    uses: ['krzema12/github-actions-typing@%s']
  'krzema12/github-actions-typing@%s':
    ref: '%s'
    commit: 'sha1-%s'
    owner_id: 1
    repo_id: 1
workflows:
  '.github/workflows/workflow.yml': ['acme/comp@%s']
`, parserlock.Version, parityTestMoved, parityTestMoved, parityTestMoved,
				parityTestSHA, parityTestSHA, parityTestSHA, parityTestSHA, parityTestMoved)
			require.NoError(t, os.WriteFile(".github/workflows/actions.lock", []byte(lock), 0o600))
			before := snapshotWorkflows(t)
			reg := &httpmock.Registry{}
			defer reg.Verify(t)
			if mode == "--verify-frozen" {
				reg.Register(httpmock.GraphQLForRepo("acme", "comp"), httpmock.JSONResponse(map[string]any{
					"data": map[string]any{"a0": testRepoResponse("acme/comp", parityTestMoved,
						"runs: {using: composite, steps: [{uses: krzema12/github-actions-typing@"+parityTestSHA+"}]}\n")},
				}))
				reg.Register(httpmock.GraphQLForRepo("krzema12", "github-actions-typing"), httpmock.JSONResponse(map[string]any{
					"data": map[string]any{"a0": testRepoResponse("typesafegithub/github-actions-typing", parityTestSHA, nodeActionYAML)},
				}))
			}
			reg.Register(httpmock.GraphQL(`commit: object\(oid`), renamedParity)
			args := []string{path}
			if mode != "default" {
				args = append([]string{mode}, args...)
			}
			payload, err := runParity(t, reg, args...)
			require.ErrorIs(t, err, errSilent)
			assert.False(t, payload.Valid)
			assertFindingCategory(t, payload, "repo-renamed")
			for _, finding := range payload.Findings {
				if finding.Category == "repo-renamed" {
					assert.Equal(t, "error", finding.Severity)
					assert.Equal(t, "acme/comp@"+parityTestMoved, finding.RequiredBy)
					assert.Contains(t, finding.Remediation, "upgrade acme/comp@"+parityTestMoved)
				}
			}
			assert.Equal(t, before, snapshotWorkflows(t))
		})
	}
}
