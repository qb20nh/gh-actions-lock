package resolve

import (
	"context"
	"strings"
	"testing"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
	"github.com/github/gh-actions-lock/internal/dep"
	"github.com/github/gh-actions-lock/internal/ghapi/httpmock"
	"github.com/github/gh-actions-lock/internal/pinpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInspectAllRecursiveFetchesSeededManifest(t *testing.T) {
	sha := strings.Repeat("a", 40)
	registry := &httpmock.Registry{}
	defer registry.Verify(t)
	registry.Register(httpmock.GraphQLForRepo("vendor", "parent"), httpmock.GraphQLQuery(
		`{"data":{"a0":{"nameWithOwner":"vendor/parent","object":{"oid":"`+sha+`","file":{"object":{"text":"runs: {using: composite, steps: [{uses: docker://alpine:3.18}]}"}}}}}}`,
		func(string, map[string]any) {}))
	r, err := New("github.com", pinpool.New(1, nil), WithTransport(registry))
	require.NoError(t, err)
	r.SeedFromLockfile([]dep.Dependency{{NWO: "vendor/parent", Ref: sha, SHA: sha}})
	_, _, err = r.InspectAllRecursive(context.Background(), []parserlock.ActionRef{{Owner: "vendor", Repo: "parent", Ref: sha}})
	require.Error(t, err)
	assert.True(t, IsInvalidMetadata(err))
	assert.Contains(t, err.Error(), "full SHA-256 digest")
}
