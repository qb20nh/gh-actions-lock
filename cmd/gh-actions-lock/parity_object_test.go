package main

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"testing"

	"github.com/github/gh-actions-lock/internal/ghapi/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParityVerifyRequiresCommitObject(t *testing.T) {
	for _, objectType := range []string{"Commit", "Blob", "Tree", "Tag"} {
		t.Run(objectType, func(t *testing.T) {
			path := writeTempWorkflow(t, `
name: ci
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@main
`, "actions/checkout@main=sha1-"+parityTestSHA)
			lockBefore := readTempLockfilePins(t)
			workflowBefore, err := os.ReadFile(path)
			require.NoError(t, err)
			reg := &httpmock.Registry{}
			defer reg.Verify(t)
			reg.Register(httpmock.GraphQL(`commit: object\(oid`), func(req *http.Request) (*http.Response, error) {
				var payload struct {
					Query     string         `json:"query"`
					Variables map[string]any `json:"variables"`
				}
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					return nil, err
				}
				assert.Equal(t, parityTestSHA, payload.Variables["oid0"])
				assert.NotContains(t, payload.Variables, "tag0", "mutable refs do not have an independent tag check")
				object := map[string]any{"oid": parityTestSHA}
				// GraphQL omits fields in a Commit fragment when the GitObject
				// is a Blob, Tree, or Tag, even though all four expose oid.
				commitFragment := regexp.MustCompile(`commit:\s*object\(oid:\s*\$oid0\)\s*\{\s*\.\.\.\s+on\s+Commit\s*\{\s*oid\s*\}`)
				if objectType != "Commit" && commitFragment.MatchString(payload.Query) {
					object = map[string]any{}
				}
				return httpmock.JSONResponse(map[string]any{"data": map[string]any{"a0": map[string]any{
					"nameWithOwner": "actions/checkout", "databaseId": 1, "commit": object,
				}}})(req)
			})
			result, err := runParity(t, reg, "--hostname", "github.com", "--verify", path)
			if objectType == "Commit" {
				require.NoError(t, err)
				assert.True(t, result.Valid)
			} else {
				require.ErrorIs(t, err, errSilent)
				assert.False(t, result.Valid)
				require.Len(t, result.Findings, 1)
				assert.Equal(t, "unreachable-pin", result.Findings[0].Category)
				assert.Equal(t, "error", result.Findings[0].Severity)
				assert.Equal(t, "actions/checkout@main", result.Findings[0].Dependency)
			}
			assert.Len(t, reg.Requests, 1)
			assert.Equal(t, lockBefore, readTempLockfilePins(t))
			workflowAfter, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, workflowBefore, workflowAfter)
		})
	}
}
