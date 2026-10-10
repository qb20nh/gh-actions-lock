package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/github/gh-actions-lock/internal/ghapi/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParitySHARootChecksRecordedClosure(t *testing.T) {
	for _, tt := range []struct {
		name     string
		category string
		child    bool
	}{
		{name: "missing repository", category: "repo-unavailable"},
		{name: "hijacked repository", category: "repo-hijacked"},
		{name: "unreachable commit", category: "unreachable-pin"},
		{name: "moved exact tag", category: "unreachable-pin"},
		{name: "missing child repository", category: "repo-unavailable", child: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, mode := range []string{"verify", "fix"} {
				t.Run(mode, func(t *testing.T) {
					path := writeTempWorkflow(t, `
name: ci
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: ACTIONS/Checkout/subpath@`+strings.ToUpper(parityTestSHA),
						"actions/checkout@v4.2.1=sha1-"+parityTestSHA)
					lock := readTempLockfilePins(t)
					if tt.child {
						lock = strings.Replace(lock, "workflows:\n", `    uses:
      - 'acme/child@v1'
  'acme/child@v1':
    ref: 'v1'
    commit: 'sha1-`+parityTestMoved+`'
    owner_id: 2
    repo_id: 2
workflows:
`, 1)
						require.NoError(t, os.WriteFile(".github/workflows/actions.lock", []byte(lock), 0o600))
					}
					before, err := os.ReadFile(path)
					require.NoError(t, err)
					root := map[string]any{
						"nameWithOwner": "actions/checkout", "databaseId": 1,
						"commit": map[string]any{"oid": parityTestSHA},
						"tag":    map[string]any{"oid": parityTestSHA},
					}
					data := map[string]any{"a0": root}
					switch tt.name {
					case "missing repository":
						data["a0"] = nil
					case "hijacked repository":
						root["databaseId"] = 99
					case "unreachable commit":
						root["commit"] = nil
					case "moved exact tag":
						root["tag"] = map[string]any{"oid": parityTestMoved}
					case "missing child repository":
						data["a1"] = nil
					}
					body, err := json.Marshal(map[string]any{"data": data})
					require.NoError(t, err)
					reg := &httpmock.Registry{}
					defer reg.Verify(t)
					reg.Register(httpmock.GraphQL(`commit: object\(oid`), httpmock.GraphQLQuery(string(body), func(_ string, variables map[string]any) {
						assert.Equal(t, parityTestSHA, variables["oid0"])
						assert.Equal(t, "v4.2.1^{commit}", variables["tag0"], "recorded exact tag remains part of parity")
						if tt.child {
							assert.Equal(t, "acme", variables["owner1"])
							assert.Equal(t, "child", variables["name1"])
							assert.Equal(t, parityTestMoved, variables["oid1"])
						}
					}))
					ct := &countingTransport{inner: reg}
					args := []string{"--hostname", "github.com", "--no-narrow", path}
					if mode == "verify" {
						args = append(args, "--verify")
					}
					p, err := runParity(t, ct, args...)
					require.ErrorIs(t, err, errSilent)
					assert.False(t, p.Valid)
					found := false
					for _, f := range p.Findings {
						if f.Category != tt.category {
							continue
						}
						found = true
						assert.Equal(t, "error", f.Severity)
						if tt.child {
							assert.Equal(t, "acme/child@v1", f.Dependency)
							assert.Equal(t, "actions/checkout@v4.2.1", f.RequiredBy)
						} else {
							assert.Equal(t, "actions/checkout@v4.2.1", f.Dependency)
						}
					}
					assert.True(t, found, "findings: %+v", p.Findings)
					assert.Equal(t, 1, ct.n, "one parity request covers the recorded closure")
					assert.Equal(t, lock, readTempLockfilePins(t))
					after, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, before, after)
				})
			}
		})
	}
}

func TestParitySHARootRenameKeepsWorkflowRefInRemediation(t *testing.T) {
	path := writeTempWorkflow(t, `
name: ci
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: ACTIONS/Checkout/subpath@`+strings.ToUpper(parityTestSHA),
		"actions/checkout@v4.2.1=sha1-"+parityTestSHA)
	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(httpmock.GraphQL(`commit: object\(oid`), httpmock.JSONResponse(map[string]any{
		"data": map[string]any{"a0": map[string]any{
			"nameWithOwner": "actions/renamed", "databaseId": 1,
			"commit": map[string]any{"oid": parityTestSHA},
			"tag":    map[string]any{"oid": parityTestSHA},
		}},
	}))
	p, err := runParity(t, reg, "--hostname", "github.com", "--verify", path)
	require.ErrorIs(t, err, errSilent)
	assert.False(t, p.Valid)
	for _, f := range p.Findings {
		if f.Category == "repo-renamed" {
			assert.Contains(t, f.Remediation, "uses: actions/renamed/subpath@"+strings.ToUpper(parityTestSHA))
			return
		}
	}
	t.Fatalf("missing rename finding: %+v", p.Findings)
}
