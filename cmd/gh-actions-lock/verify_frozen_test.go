package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
	"github.com/github/gh-actions-lock/internal/ghapi/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyFrozenInspectsDefiningCommitGraph(t *testing.T) {
	const rootSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const childSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, definition := range []string{"composite", "reusable workflow"} {
		for _, tc := range []struct {
			name       string
			childRef   string
			recordEdge bool
			wantValid  bool
			category   string
		}{
			{name: "complete immutable graph", childRef: childSHA, recordEdge: true, wantValid: true},
			{name: "omitted mutable child", childRef: "main", category: "mutable-reference"},
			{name: "recorded release tag remains mutable", childRef: "v1.2.3", recordEdge: true, category: "mutable-reference"},
			{name: "omitted immutable child", childRef: childSHA, category: "not-pinned"},
		} {
			t.Run(definition+"/"+tc.name, func(t *testing.T) {
				workflow := "jobs: {test: {runs-on: ubuntu-latest, steps: [{uses: vendor/parent@" + rootSHA + "}]}}\n"
				rootManifest := "runs: {using: composite, steps: [{uses: $/actions/leaf}]}\n"
				leafManifest := "runs: {using: composite, steps: [{uses: vendor/child@" + tc.childRef + "}]}\n"
				rootPath, leafPath := "action.yml", "actions/leaf/action.yml"
				if definition == "reusable workflow" {
					workflow = "jobs: {call: {uses: vendor/parent/.github/workflows/root.yml@" + rootSHA + "}}\n"
					rootManifest = "jobs: {call: {uses: ./.github/workflows/leaf.yml}}\n"
					leafManifest = "jobs: {test: {runs-on: ubuntu-latest, steps: [{uses: vendor/child@" + tc.childRef + "}]}}\n"
					rootPath, leafPath = ".github/workflows/root.yml", ".github/workflows/leaf.yml"
				}
				path := writeTempWorkflow(t, workflow, "vendor/parent@"+rootSHA+"=sha1-"+rootSHA)
				require.NoError(t, os.Mkdir(".git", 0o755))
				if tc.recordEdge {
					lock := fmt.Sprintf("version: %s\ndependencies:\n  'vendor/parent@%s':\n    ref: '%s'\n    commit: 'sha1-%s'\n    owner_id: 1\n    repo_id: 1\n    uses: ['vendor/child@%s']\n  'vendor/child@%s':\n    ref: '%s'\n    commit: 'sha1-%s'\n    owner_id: 1\n    repo_id: 1\nworkflows:\n  '.github/workflows/workflow.yml': ['vendor/parent@%s']\n", parserlock.Version, rootSHA, rootSHA, rootSHA, tc.childRef, tc.childRef, tc.childRef, childSHA, rootSHA)
					require.NoError(t, os.WriteFile(".github/workflows/actions.lock", []byte(lock), 0o600))
				}
				before := snapshotWorkflows(t)
				if tc.wantValid {
					absolutePath, err := filepath.Abs(path)
					require.NoError(t, err)
					path = absolutePath
				}
				registry := &httpmock.Registry{}
				defer registry.Verify(t)
				visited := map[string]bool{}
				resolveManifest := func(req *http.Request) (*http.Response, error) {
					var body struct {
						Variables map[string]any `json:"variables"`
					}
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					data := map[string]any{}
					for i := 0; ; i++ {
						repo, ok := body.Variables[fmt.Sprintf("name%d", i)].(string)
						if !ok {
							break
						}
						manifestPath := body.Variables[fmt.Sprintf("yml%d", i)].(string)
						expression := body.Variables[fmt.Sprintf("expr%d", i)].(string)
						sha, content := childSHA, nodeActionYAML
						if repo == "parent" {
							assert.Equal(t, rootSHA+"^{commit}", expression, "sibling manifests must come from the defining commit")
							sha = rootSHA
							if manifestPath == rootPath {
								content = rootManifest
							} else {
								assert.Equal(t, leafPath, manifestPath)
								content = leafManifest
							}
						}
						visited[repo+"/"+manifestPath] = true
						data[fmt.Sprintf("a%d", i)] = testRepoResponse("vendor/"+repo, sha, content)
					}
					return httpmock.JSONResponse(map[string]any{"data": data})(req)
				}
				for range 3 {
					registry.Register(httpmock.GraphQL(`object\(expression`), resolveManifest)
				}
				payload, err := runParity(t, registry, "--verify-frozen", path)
				if tc.wantValid {
					require.NoError(t, err, "%+v", payload.Findings)
					assert.Empty(t, payload.Findings)
				} else {
					require.ErrorIs(t, err, errSilent)
					assertFindingCategory(t, payload, tc.category)
				}
				assert.Equal(t, tc.wantValid, payload.Valid)
				assert.True(t, visited["parent/"+leafPath], "recorded parents must still have their actual sibling manifests inspected")
				assert.Equal(t, before, snapshotWorkflows(t), "frozen verification must not modify workflows or lockfiles")
			})
		}
	}
}

func TestVerifyFrozenFailsClosedOnUnreadableManifest(t *testing.T) {
	sha := strings.Repeat("a", 40)
	path := writeTempWorkflow(t, "jobs: {test: {runs-on: ubuntu-latest, steps: [{uses: vendor/parent@"+sha+"}]}}\n", "vendor/parent@"+sha+"=sha1-"+sha)
	require.NoError(t, os.Mkdir(".git", 0o755))
	before := snapshotWorkflows(t)
	registry := &httpmock.Registry{}
	defer registry.Verify(t)
	registry.Register(httpmock.GraphQL(`object\(expression`), httpmock.JSONResponse(map[string]any{
		"data": map[string]any{"a0": testRepoResponse("vendor/parent", sha, "runs: [\n")},
	}))
	payload, err := runParity(t, registry, "--verify-frozen", path)
	require.ErrorIs(t, err, errSilent)
	assert.False(t, payload.Valid)
	assertFindingCategory(t, payload, "reachability-unknown")
	assert.Equal(t, before, snapshotWorkflows(t))
}

func assertFindingCategory(t *testing.T, payload parityPayload, category string) {
	t.Helper()
	var categories []string
	for _, f := range payload.Findings {
		categories = append(categories, f.Category)
	}
	assert.Contains(t, categories, category)
}

func TestVerifyFrozenRejectsUnsupportedAndMutableContainerUses(t *testing.T) {
	for _, tc := range []struct {
		use   string
		valid bool
	}{
		{use: "invalid"},
		{use: "docker://alpine:3.18"},
		{use: "docker://alpine@sha256:abcdef"},
		{use: "docker://alpine@sha256:" + strings.Repeat("a", 64), valid: true},
	} {
		t.Run(tc.use, func(t *testing.T) {
			path := writeTempWorkflow(t, "jobs: {test: {runs-on: ubuntu-latest, steps: [{uses: "+tc.use+"}]}}\n")
			require.NoError(t, os.Mkdir(".git", 0o755))
			require.NoError(t, os.WriteFile(".github/workflows/actions.lock", []byte(emptyDependencyLockfile), 0o600))
			registry := &httpmock.Registry{}
			defer registry.Verify(t)
			payload, err := runParity(t, registry, "--verify-frozen", path)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, errSilent)
			}
			assert.Equal(t, tc.valid, payload.Valid)
		})
	}
}

func TestVerifyLocalInventoryUsesActualDirectRefs(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, ref := range []string{"v1", sha} {
		t.Run(ref, func(t *testing.T) {
			path := writeTempWorkflow(t, "jobs: {test: {runs-on: ubuntu-latest, steps: [{uses: vendor/action@"+ref+"}]}}\n", "vendor/action@v1=sha1-"+sha)
			require.NoError(t, os.Mkdir(".git", 0o755))
			stdout, _, err := runCommandWithHTTP(t, &httpmock.Registry{}, "--verify-local", "--json=dependencies", path)
			require.NoError(t, err)
			var payload struct {
				Dependencies []struct {
					Ref    string `json:"ref"`
					SHA    string `json:"sha"`
					Direct bool   `json:"direct"`
				} `json:"dependencies"`
			}
			require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
			require.Len(t, payload.Dependencies, 1)
			assert.Equal(t, ref, payload.Dependencies[0].Ref)
			assert.Equal(t, sha, payload.Dependencies[0].SHA)
			assert.True(t, payload.Dependencies[0].Direct)
		})
	}
}

func TestVerifyFrozenRejectsRepositoryRenameWithoutLosingCoverage(t *testing.T) {
	path := writeTempWorkflow(t, "jobs: {test: {runs-on: ubuntu-latest, steps: [{uses: krzema12/github-actions-typing@"+parityTestSHA+"}]}}\n", "krzema12/github-actions-typing@"+parityTestSHA+"=sha1-"+parityTestSHA)
	require.NoError(t, os.Mkdir(".git", 0o755))
	before := snapshotWorkflows(t)
	registry := &httpmock.Registry{}
	defer registry.Verify(t)
	registry.Register(httpmock.GraphQL(`object\(expression`), httpmock.JSONResponse(map[string]any{
		"data": map[string]any{"a0": testRepoResponse("typesafegithub/github-actions-typing", parityTestSHA, nodeActionYAML)},
	}))
	state := map[string]any{
		"nameWithOwner": "typesafegithub/github-actions-typing",
		"databaseId":    1,
		"owner":         map[string]any{"databaseId": 42},
		"commit":        map[string]any{"oid": parityTestSHA},
		"tag":           map[string]any{"oid": parityTestSHA},
	}
	registry.Register(httpmock.GraphQL(`commit: object\(oid`), httpmock.JSONResponse(map[string]any{
		"data": map[string]any{"a0": state, "a1": state},
	}))
	payload, err := runParity(t, registry, "--verify-frozen", path)
	require.ErrorIs(t, err, errSilent)
	assert.False(t, payload.Valid)
	assertFindingCategory(t, payload, "repo-renamed")
	assert.Equal(t, before, snapshotWorkflows(t))
	for _, finding := range payload.Findings {
		assert.NotEqual(t, "not-pinned", finding.Category)
	}
}
