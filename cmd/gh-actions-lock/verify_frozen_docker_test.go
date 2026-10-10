package main

import (
	"os"
	"strings"
	"testing"

	"github.com/github/gh-actions-lock/internal/ghapi/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyFrozenDockerRequiresStepLevelUses(t *testing.T) {
	use := "docker://alpine@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name     string
		workflow string
		valid    bool
	}{
		{
			name:     "job reference is invalid",
			workflow: "jobs: {test: {uses: " + use + "}}\n",
		},
		{
			name:     "step reference is valid",
			workflow: "jobs: {test: {runs-on: ubuntu-latest, steps: [{uses: " + use + "}]}}\n",
			valid:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempWorkflow(t, tc.workflow)
			require.NoError(t, os.Mkdir(".git", 0o755))
			require.NoError(t, os.WriteFile(".github/workflows/actions.lock", []byte(emptyDependencyLockfile), 0o600))
			before := snapshotWorkflows(t)
			registry := &httpmock.Registry{}
			defer registry.Verify(t)

			payload, err := runParity(t, registry, "--verify-frozen", path)
			if tc.valid {
				require.NoError(t, err)
				assert.Empty(t, payload.Findings)
			} else {
				require.ErrorIs(t, err, errSilent)
				assertFindingCategory(t, payload, "not-pinned")
			}
			assert.Equal(t, tc.valid, payload.Valid)
			assert.Equal(t, before, snapshotWorkflows(t))
		})
	}
}
