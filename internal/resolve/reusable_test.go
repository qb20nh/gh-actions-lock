package resolve

import (
	"context"
	"fmt"
	"strings"
	"testing"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
	"github.com/github/gh-actions-lock/internal/dep"
	"github.com/github/gh-actions-lock/internal/ghapi"
	"github.com/github/gh-actions-lock/internal/ghapi/httpmock"
	"github.com/github/gh-actions-lock/internal/pinpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveAllRecursiveReusableWorkflowClosure(t *testing.T) {
	const rootSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const nextSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	r := seedCache(&Resolver{MaxRecursionDepth: DefaultMaxRecursionDepth}, map[ghapi.ActionRef]resolvedEntry{
		ghapi.ForActionRef("vendor", "flows", ".github/workflows/root.yml", "v1"): {
			dep:       dep.Dependency{NWO: "vendor/flows", Path: ".github/workflows/root.yml", Ref: "v1", SHA: rootSHA},
			actionYML: "jobs: {call: {uses: ./.github/workflows/local.yaml}}\n",
		},
		ghapi.ForActionRef("vendor", "flows", ".github/workflows/local.yaml", rootSHA): {
			dep:       dep.Dependency{NWO: "vendor/flows", Path: ".github/workflows/local.yaml", Ref: rootSHA, SHA: rootSHA},
			actionYML: "jobs: {call: {uses: other/flows/.github/workflows/next.yml@v2}}\n",
		},
		ghapi.ForActionRef("other", "flows", ".github/workflows/next.yml", "v2"): {
			dep:       dep.Dependency{NWO: "other/flows", Path: ".github/workflows/next.yml", Ref: "v2", SHA: nextSHA},
			actionYML: "jobs: {build: {steps: [{uses: $/actions/own}]}}\n",
		},
		ghapi.ForActionRef("other", "flows", "actions/own", nextSHA): {
			dep:       dep.Dependency{NWO: "other/flows", Path: "actions/own", Ref: nextSHA, SHA: nextSHA},
			actionYML: "runs: {using: composite, steps: [{uses: actions/checkout@v4}]}\n",
		},
		ghapi.ForActionRef("actions", "checkout", "", "v4"): {
			dep:       dep.Dependency{NWO: "actions/checkout", Ref: "v4", SHA: strings.Repeat("c", 40)},
			actionYML: "runs: {using: node20}\n",
		},
	})
	deps, parents, err := r.ResolveAllRecursive(context.Background(), []parserlock.ActionRef{{Owner: "vendor", Repo: "flows", Path: ".github/workflows/root.yml", Ref: "v1"}})
	require.NoError(t, err)
	require.Len(t, deps, 3)
	assert.Equal(t, rootSHA, deps[0].SHA)
	assert.Equal(t, "v1", deps[0].Ref)
	assert.Equal(t, []string{"vendor/flows@v1"}, parents["other/flows@v2"])
	assert.Equal(t, []string{"other/flows@v2"}, parents["actions/checkout@v4"])
	assert.Empty(t, parents["vendor/flows@v1"], "local definitions share their parent's tarball")
}

func TestResolveAllRecursiveRejectsIncompleteMetadata(t *testing.T) {
	for _, tt := range []struct {
		name    string
		path    string
		content string
	}{
		{name: "malformed action", content: "runs: [\n"},
		{name: "unsupported execution", content: "runs: {using: unknown}\n"},
		{name: "missing execution", content: "name: incomplete\n"},
		{name: "missing workflow jobs", path: ".github/workflows/called.yml", content: "name: incomplete\n"},
		{name: "hidden step mapping", content: "runs: {using: composite, steps: {uses: vendor/action@v1}}\n"},
		{name: "invalid nested reference", content: "runs: {using: composite, steps: [{uses: invalid}]}\n"},
		{name: "invalid local workflow", path: ".github/workflows/called.yml", content: "jobs: {call: {uses: ./outside.yml}}\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := seedCache(&Resolver{MaxRecursionDepth: DefaultMaxRecursionDepth}, map[ghapi.ActionRef]resolvedEntry{
				ghapi.ForActionRef("vendor", "repo", tt.path, "v1"): {
					dep:       dep.Dependency{NWO: "vendor/repo", Path: tt.path, Ref: "v1", SHA: strings.Repeat("a", 40)},
					actionYML: tt.content,
				},
			})
			deps, _, err := r.ResolveAllRecursive(context.Background(), []parserlock.ActionRef{{Owner: "vendor", Repo: "repo", Path: tt.path, Ref: "v1"}})
			require.Error(t, err)
			assert.True(t, IsInvalidMetadata(err), "%v", err)
			require.Len(t, deps, 1, "preserve resolution evidence while failing incomplete coverage")
		})
	}
}

func TestResolveFreshMetadataFailureIsBlocking(t *testing.T) {
	for name, body := range map[string]string{
		"missing file": `{"data":{"a0":{"object":{"oid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","file":null,"fileYaml":null}}}}`,
		"invalid YAML": `{"data":{"a0":{"object":{"oid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","file":{"object":{"text":"jobs: ["}}}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			registry := &httpmock.Registry{}
			defer registry.Verify(t)
			registry.Register(httpmock.GraphQLForRepo("vendor", "flows"), httpmock.GraphQLQuery(body, func(string, map[string]any) {}))
			r, err := New("github.com", pinpool.New(1, nil), WithTransport(registry))
			require.NoError(t, err)
			_, _, err = r.ResolveAllRecursive(context.Background(), []parserlock.ActionRef{{Owner: "vendor", Repo: "flows", Path: ".github/workflows/called.yml", Ref: "v1"}})
			require.Error(t, err)
			assert.True(t, IsInvalidMetadata(err), "API metadata errors must block incomplete coverage: %v", err)
		})
	}
}

func TestResolveReusableWorkflowRejectsCallerWorkspaceAction(t *testing.T) {
	r := seedCache(&Resolver{MaxRecursionDepth: DefaultMaxRecursionDepth}, map[ghapi.ActionRef]resolvedEntry{
		ghapi.ForActionRef("vendor", "flows", ".github/workflows/called.yml", "v1"): {
			dep:       dep.Dependency{NWO: "vendor/flows", Path: ".github/workflows/called.yml", Ref: "v1", SHA: strings.Repeat("a", 40)},
			actionYML: "jobs: {build: {steps: [{uses: ./actions/caller-owned}]}}\n",
		},
	})
	_, _, err := r.ResolveAllRecursive(context.Background(), []parserlock.ActionRef{{Owner: "vendor", Repo: "flows", Path: ".github/workflows/called.yml", Ref: "v1"}})
	require.Error(t, err)
	assert.True(t, IsCompositeLocalPath(err))
}

func TestResolveReusableWorkflowRejectsSameTarballCallCycle(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	r := seedCache(&Resolver{MaxRecursionDepth: DefaultMaxRecursionDepth}, map[ghapi.ActionRef]resolvedEntry{
		ghapi.ForActionRef("vendor", "flows", ".github/workflows/root.yml", "v1"): {
			dep:       dep.Dependency{NWO: "vendor/flows", Path: ".github/workflows/root.yml", Ref: "v1", SHA: sha},
			actionYML: "jobs: {call: {uses: ./.github/workflows/next.yml}}\n",
		},
		ghapi.ForActionRef("vendor", "flows", ".github/workflows/next.yml", sha): {
			dep:       dep.Dependency{NWO: "vendor/flows", Path: ".github/workflows/next.yml", Ref: sha, SHA: sha},
			actionYML: "jobs: {call: {uses: $/.github/workflows/root.yml}}\n",
		},
	})
	deps, parents, err := r.ResolveAllRecursive(context.Background(), []parserlock.ActionRef{{Owner: "vendor", Repo: "flows", Path: ".github/workflows/root.yml", Ref: "v1"}})
	require.Error(t, err)
	assert.True(t, IsInvalidMetadata(err))
	assert.Contains(t, err.Error(), "reusable workflow call cycle")
	require.Len(t, deps, 1)
	assert.Empty(t, parents, "cycle detection must retain the tarball-level graph invariant")
}

func TestResolvePinnedWorkflowIgnoresMovedLogicalRefCache(t *testing.T) {
	const pinnedSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const movedSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	registry := &httpmock.Registry{}
	defer registry.Verify(t)
	registry.Register(httpmock.GraphQLForRepo("vendor", "flows"), httpmock.GraphQLQuery(
		fmt.Sprintf(`{"data":{"a0":{"object":{"oid":%q,"file":{"object":{"text":%q}}}}}}`, pinnedSHA, "jobs: {build: {steps: [{uses: vendor/good@v1}]}}\n"),
		func(_ string, variables map[string]any) {
			assert.Equal(t, pinnedSHA+"^{commit}", variables["expr0"])
			assert.Equal(t, ".github/workflows/shared.yml", variables["yml0"])
		}))
	r, err := New("github.com", pinpool.New(1, nil), WithTransport(registry))
	require.NoError(t, err)
	seedCache(r, map[ghapi.ActionRef]resolvedEntry{
		ghapi.ForActionRef("vendor", "flows", ".github/workflows/root.yml", "main"): {
			dep:       dep.Dependency{NWO: "vendor/flows", Path: ".github/workflows/root.yml", Ref: "main", SHA: pinnedSHA},
			actionYML: "jobs: {call: {uses: ./.github/workflows/shared.yml}}\n",
		},
		ghapi.ForActionRef("vendor", "flows", ".github/workflows/shared.yml", "main"): {
			dep:       dep.Dependency{NWO: "vendor/flows", Path: ".github/workflows/shared.yml", Ref: "main", SHA: movedSHA},
			actionYML: "jobs: {build: {steps: [{uses: vendor/moved@v1}]}}\n",
		},
		ghapi.ForActionRef("vendor", "good", "", "v1"): {
			dep:       dep.Dependency{NWO: "vendor/good", Ref: "v1", SHA: strings.Repeat("c", 40)},
			actionYML: "runs: {using: node20}\n",
		},
		ghapi.ForActionRef("vendor", "moved", "", "v1"): {
			dep:       dep.Dependency{NWO: "vendor/moved", Ref: "v1", SHA: strings.Repeat("d", 40)},
			actionYML: "runs: {using: node20}\n",
		},
	})
	deps, _, err := r.ResolveAllRecursive(context.Background(), []parserlock.ActionRef{{Owner: "vendor", Repo: "flows", Path: ".github/workflows/root.yml", Ref: "main"}})
	require.NoError(t, err)
	require.Len(t, deps, 2)
	assert.Equal(t, "vendor/good", deps[1].NWO)
	logical, ok := r.cache.Get(ghapi.ForActionRef("vendor", "flows", ".github/workflows/shared.yml", "main"))
	require.True(t, ok)
	assert.Equal(t, movedSHA, logical.dep.SHA, "pinned traversal must not overwrite the mutable-ref cache")
	deps, _, err = r.ResolveAllRecursive(context.Background(), []parserlock.ActionRef{{Owner: "vendor", Repo: "flows", Path: ".github/workflows/shared.yml", Ref: "main"}})
	require.NoError(t, err)
	require.Len(t, deps, 2)
	assert.Equal(t, "vendor/moved", deps[1].NWO)
}

func TestResolveAllRecursiveRejectsConflictingSubpathPins(t *testing.T) {
	for _, reversed := range []bool{false, true} {
		t.Run(fmt.Sprintf("reversed=%t", reversed), func(t *testing.T) {
			r := seedCache(&Resolver{MaxRecursionDepth: DefaultMaxRecursionDepth}, map[ghapi.ActionRef]resolvedEntry{
				ghapi.ForActionRef("vendor", "repo", "a", "main"): {dep: dep.Dependency{NWO: "Vendor/Repo", Path: "a", Ref: "main", SHA: strings.Repeat("a", 40)}, actionYML: "runs: {using: node20}\n"},
				ghapi.ForActionRef("vendor", "repo", "b", "main"): {dep: dep.Dependency{NWO: "vendor/repo", Path: "b", Ref: "main", SHA: strings.Repeat("b", 40)}, actionYML: "runs: {using: node20}\n"},
			})
			refs := []parserlock.ActionRef{{Owner: "vendor", Repo: "repo", Path: "a", Ref: "main"}, {Owner: "vendor", Repo: "repo", Path: "b", Ref: "main"}}
			if reversed {
				refs[0], refs[1] = refs[1], refs[0]
			}
			_, _, err := r.ResolveAllRecursive(context.Background(), refs)
			require.Error(t, err)
			assert.True(t, IsConflictingRef(err), "%v", err)
		})
	}
}
