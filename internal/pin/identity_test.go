package pin

import (
	"context"
	"strings"
	"testing"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
	"github.com/github/gh-actions-lock/internal/dep"
	"github.com/github/gh-actions-lock/internal/ghapi/httpmock"
	"github.com/github/gh-actions-lock/internal/lockfile"
	"github.com/github/gh-actions-lock/internal/pinpool"
	"github.com/github/gh-actions-lock/internal/pipeline/checks"
	"github.com/github/gh-actions-lock/internal/resolve"
	"github.com/github/gh-actions-lock/internal/tag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPruneStaleInventory_PreservesDistinctRefCase(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	inventory := []checks.InventoryEntry{
		{Dep: dep.Dependency{NWO: "Owner/Action", Ref: "Main", SHA: sha}, Direct: true},
		{Dep: dep.Dependency{NWO: "owner/action", Ref: "main", SHA: sha}, Direct: true},
	}
	findings := []checks.Finding{{
		Category: checks.Stale,
		Dependency: &dep.Dependency{
			NWO: "OWNER/ACTION", Ref: "main", SHA: strings.ToUpper(sha),
		},
	}}

	got := pruneStaleInventory(inventory, findings, false, false)

	require.Len(t, got, 1)
	assert.Equal(t, "Main", got[0].Dep.Ref)
	assert.Equal(t, sha, got[0].Dep.SHA)
}

func TestBuildPinnedEntries_DeduplicatesOnlyMatchingTargets(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	old := dep.Dependency{Hostname: "github.com", NWO: "Owner/Action", Ref: "v1", SHA: sha}
	_, targets := partitionByInventory([]checks.InventoryEntry{{Dep: old}}, nil)
	tests := []struct {
		name string
		dep  dep.Dependency
		want int
	}{
		{name: "same target", dep: dep.Dependency{NWO: "owner/action", Ref: "v1", SHA: sha}, want: 0},
		{name: "distinct ref sharing SHA", dep: dep.Dependency{NWO: "owner/action", Ref: "v2", SHA: sha}, want: 1},
		{name: "distinct ref case", dep: dep.Dependency{NWO: "owner/action", Ref: "V1", SHA: sha}, want: 1},
		{name: "distinct host", dep: dep.Dependency{Hostname: "tenant.ghe.com", NWO: "owner/action", Ref: "v1", SHA: sha}, want: 1},
		{name: "distinct commit", dep: dep.Dependency{NWO: "owner/action", Ref: "v1", SHA: strings.Repeat("b", 40)}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := []dep.Dependency{tt.dep}
			refs := []parserlock.ActionRef{{Owner: "owner", Repo: "action", Ref: tt.dep.Ref}}
			got := buildPinnedEntries(PlanOptions{}, checks.WorkflowReport{Path: "ci.yml"}, deps, nil, lockfile.NewDirectTracker(refs, deps), targets)
			require.Len(t, got, tt.want)
			if tt.want > 0 {
				assert.Equal(t, tt.dep.Ref, got[0].Ref)
				assert.True(t, got[0].Direct)
			}
		})
	}
}

func TestPlanWorkflow_PreservesSameSHAWithDistinctRefs(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(httpmock.GraphQLForRepo("owner", "action"), httpmock.JSONResponse(map[string]any{
		"data": map[string]any{
			"a0": identityActionResponse("owner/action", sha, "name: Action\nruns:\n  using: node20\n"),
		},
	}))
	pool := pinpool.New(2, nil)
	resolver, err := resolve.New("github.com", pool, resolve.WithTransport(reg))
	require.NoError(t, err)
	wr := checks.WorkflowReport{
		Path: ".github/workflows/ci.yml",
		ActionRefs: []parserlock.ActionRef{
			{Owner: "owner", Repo: "action", Ref: "v1.0.0"},
			{Owner: "owner", Repo: "action", Ref: "v2.0.0"},
		},
		Inventory: []checks.InventoryEntry{{
			Dep: dep.Dependency{NWO: "owner/action", Ref: "v1.0.0", SHA: sha}, Direct: true,
		}},
		Findings: []checks.Finding{{Category: checks.RefChanged, Severity: checks.SeverityError}},
	}

	got, err := planWorkflow(context.Background(), wr, PlanOptions{Resolver: resolver, Pool: pool, NoNarrow: true}, func(string) {})

	require.NoError(t, err)
	require.Len(t, got.entries, 2)
	assert.Equal(t, "v1.0.0", got.entries[0].Ref)
	assert.Equal(t, Verified, got.entries[0].Resolution)
	assert.Equal(t, "v2.0.0", got.entries[1].Ref)
	assert.Equal(t, Pinned, got.entries[1].Resolution)
	assert.True(t, got.entries[1].Direct)
	assert.Equal(t, sha, got.entries[1].SHA)
}

func TestPlanWorkflow_PreservesTrustedCaseSensitiveRef(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	wr := checks.WorkflowReport{
		Path:       ".github/workflows/ci.yml",
		ActionRefs: []parserlock.ActionRef{{Owner: "owner", Repo: "action", Ref: "Main"}},
		Inventory: []checks.InventoryEntry{
			{Dep: dep.Dependency{NWO: "owner/action", Ref: "Main", SHA: sha}, Direct: true},
			{Dep: dep.Dependency{NWO: "owner/action", Ref: "main", SHA: sha}, Direct: true},
		},
		Findings: []checks.Finding{{
			Category: checks.Stale, Severity: checks.SeverityWarning,
			Dependency: &dep.Dependency{NWO: "owner/action", Ref: "main", SHA: sha},
		}},
	}

	got, err := planWorkflow(context.Background(), wr, PlanOptions{}, func(string) {})

	require.NoError(t, err)
	require.Len(t, got.entries, 1)
	assert.Equal(t, "Main", got.entries[0].Ref)
	assert.Equal(t, sha, got.entries[0].SHA)
	assert.Equal(t, Verified, got.entries[0].Resolution)
}

func TestPlanWorkflow_PreservesRefSharedWithRemoteParent(t *testing.T) {
	const sha = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, ref := range []string{"v4", sha} {
		t.Run(ref, func(t *testing.T) {
			reg := &httpmock.Registry{}
			defer reg.Verify(t)
			reg.Register(httpmock.GraphQLForRepo("owner", "composite"), httpmock.JSONResponse(map[string]any{
				"data": map[string]any{
					"a0": identityActionResponse("owner/composite", strings.Repeat("a", 40), "name: Parent\nruns:\n  using: composite\n  steps:\n    - uses: actions/checkout@"+ref+"\n"),
					"a1": identityActionResponse("actions/checkout", sha, "name: Checkout\nruns:\n  using: node20\n"),
				},
			}))
			if ref == sha {
				reg.Register(httpmock.REST("GET", `repos/actions/checkout$`), httpmock.JSONResponse(map[string]any{"default_branch": "main"}))
				reg.Register(httpmock.REST("GET", `repos/actions/checkout/git/ref/heads/main`), httpmock.JSONResponse(map[string]any{
					"ref": "refs/heads/main", "object": map[string]any{"sha": sha, "type": "commit"},
				}))
				reg.Register(httpmock.REST("GET", `repos/actions/checkout/tags`), httpmock.JSONResponse(httpmock.TagListResponse("v4.2.1", sha)))
			}
			pool := pinpool.New(2, nil)
			resolver, err := resolve.New("github.com", pool, resolve.WithTransport(reg))
			require.NoError(t, err)
			tagger := tag.NewListerForTest(t, reg)
			if ref == "v4" {
				reg.Register(httpmock.REST("GET", `repos/actions/checkout/tags`), httpmock.JSONResponse(httpmock.TagListResponse("v4", sha, "v4.2.1", sha)))
				reg.Register(httpmock.REST("GET", `repos/actions/checkout/releases`), httpmock.JSONResponse([]map[string]any{}))
				patch, err := tagger.BestPatchTagForSHA(context.Background(), "actions", "checkout", sha, ref)
				require.NoError(t, err)
				require.Equal(t, "v4.2.1", patch)
			}
			wr := checks.WorkflowReport{
				Path: ".github/workflows/ci.yml",
				ActionRefs: []parserlock.ActionRef{
					{Owner: "owner", Repo: "composite", Ref: "v1.0.0"},
					{Owner: "actions", Repo: "checkout", Ref: ref},
				},
				Findings: []checks.Finding{{Category: checks.NotPinned, Severity: checks.SeverityError}},
			}
			got, err := planWorkflow(context.Background(), wr, PlanOptions{
				Resolver: resolver, Pool: pool, Tagger: tagger,
			}, func(string) {})
			require.NoError(t, err)
			require.Len(t, got.entries, 2)
			for _, entry := range got.entries {
				if entry.NWO != "actions/checkout" {
					continue
				}
				assert.Equal(t, ref, entry.Ref)
				assert.Equal(t, sha, entry.SHA)
				assert.True(t, entry.Direct)
				assert.Equal(t, []string{"owner/composite@v1.0.0"}, entry.RequiredBy)
			}
			require.Len(t, got.wplans, 1)
			assert.Empty(t, got.wplans[0].Rewrites)
		})
	}
}

func TestNarrowVerifiedEntries_PreservesRemoteParentRef(t *testing.T) {
	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	sha := strings.Repeat("a", 40)
	reg.Register(httpmock.REST("GET", `repos/actions/checkout/tags`), httpmock.JSONResponse(httpmock.TagListResponse("v4.2.1", sha)))
	reg.Register(httpmock.REST("GET", `repos/actions/checkout/releases`), httpmock.JSONResponse([]map[string]any{}))
	tagger := tag.NewListerForTest(t, reg)
	patch, err := tagger.BestPatchTagForSHA(context.Background(), "actions", "checkout", sha, "v4")
	require.NoError(t, err)
	require.Equal(t, "v4.2.1", patch)
	entries := []Entry{{
		NWO: "actions/checkout", Ref: "v4", SHA: sha,
		Direct: true, RequiredBy: []string{"owner/composite@v1.0.0"}, Resolution: Verified,
	}}
	refs := []parserlock.ActionRef{{Owner: "actions", Repo: "checkout", Ref: "v4"}}

	got := narrowVerifiedEntries(context.Background(), entries, PlanOptions{Tagger: tagger}, refs)

	assert.Empty(t, got)
	assert.Equal(t, "v4", entries[0].Ref)
}

func TestPlanWorkflow_AddsNewParentToExistingDirectPin(t *testing.T) {
	const sha = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(httpmock.GraphQLForRepo("owner", "composite"), httpmock.JSONResponse(map[string]any{
		"data": map[string]any{
			"a0": identityActionResponse("owner/composite", strings.Repeat("a", 40), "name: Parent\nruns:\n  using: composite\n  steps:\n    - uses: actions/checkout@v4\n"),
		},
	}))
	reg.Register(httpmock.GraphQLForRepo("actions", "checkout"), httpmock.JSONResponse(map[string]any{
		"data": map[string]any{
			"a0": identityActionResponse("actions/checkout", sha, "name: Checkout\nruns:\n  using: node20\n"),
		},
	}))
	reg.Register(httpmock.REST("GET", `repos/actions/checkout/tags`), httpmock.JSONResponse(httpmock.TagListResponse("v4.2.1", sha)))
	reg.Register(httpmock.REST("GET", `repos/actions/checkout/releases`), httpmock.JSONResponse([]map[string]any{}))
	tagger := tag.NewListerForTest(t, reg)
	patch, err := tagger.BestPatchTagForSHA(context.Background(), "actions", "checkout", sha, "v4")
	require.NoError(t, err)
	require.Equal(t, "v4.2.1", patch)
	pool := pinpool.New(2, nil)
	resolver, err := resolve.New("github.com", pool, resolve.WithTransport(reg))
	require.NoError(t, err)
	wr := checks.WorkflowReport{
		Path: ".github/workflows/ci.yml",
		ActionRefs: []parserlock.ActionRef{
			{Owner: "actions", Repo: "checkout", Ref: "v4"},
			{Owner: "owner", Repo: "composite", Ref: "v1.0.0"},
		},
		Inventory: []checks.InventoryEntry{{
			Dep: dep.Dependency{NWO: "actions/checkout", Ref: "v4", SHA: sha}, Direct: true,
		}},
		Findings: []checks.Finding{{Category: checks.NotPinned, Severity: checks.SeverityError}},
	}

	got, err := planWorkflow(context.Background(), wr, PlanOptions{Resolver: resolver, Pool: pool, Tagger: tagger}, func(string) {})

	require.NoError(t, err)
	require.Len(t, got.entries, 2)
	assert.Equal(t, "v4", got.entries[0].Ref)
	assert.Equal(t, Verified, got.entries[0].Resolution)
	assert.Equal(t, []string{"owner/composite@v1.0.0"}, got.entries[0].RequiredBy)
	assert.True(t, got.entries[0].Direct)
	assert.Empty(t, got.wplans[0].Rewrites)
}

func identityActionResponse(nwo, sha, manifest string) map[string]any {
	return map[string]any{
		"nameWithOwner": nwo,
		"object": map[string]any{
			"oid":  sha,
			"file": map[string]any{"object": map[string]any{"text": manifest}},
		},
	}
}
