package ghapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/github/gh-actions-lock/internal/ghapi/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseByTag(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload map[string]any
		status  int
		wantErr string
	}{
		{name: "published", payload: map[string]any{"tag_name": "v1.2.3", "published_at": "2026-01-01T00:00:00Z", "immutable": true}},
		{name: "draft", payload: map[string]any{"tag_name": "v1.2.3", "published_at": "2026-01-01T00:00:00Z", "draft": true}, wantErr: "no published release date"},
		{name: "unpublished", payload: map[string]any{"tag_name": "v1.2.3"}, wantErr: "no published release date"},
		{name: "different tag", payload: map[string]any{"tag_name": "v1.2.2", "published_at": "2026-01-01T00:00:00Z"}, wantErr: "returned tag"},
		{name: "missing release", status: http.StatusNotFound, wantErr: "fetching release"},
		{name: "API failure", status: http.StatusInternalServerError, wantErr: "fetching release"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := &httpmock.Registry{}
			t.Cleanup(func() { registry.Verify(t) })
			response := httpmock.JSONResponse(tc.payload)
			if tc.status != 0 {
				response = httpmock.StatusResponse(tc.status)
			}
			registry.Register(httpmock.REST("GET", `^/repos/o/r/releases/tags/v1\.2\.3$`), response)
			client, err := New("github.com", WithClientTransport(registry))
			require.NoError(t, err)
			release, err := client.ReleaseByTag(context.Background(), "o", "r", "v1.2.3")
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "v1.2.3", release.TagName)
			assert.Equal(t, "2026-01-01T00:00:00Z", release.PublishedAt)
			assert.True(t, release.Immutable)
		})
	}
}

func TestReleaseByTagEscapesRef(t *testing.T) {
	registry := &httpmock.Registry{}
	t.Cleanup(func() { registry.Verify(t) })
	registry.Register(func(req *http.Request) bool {
		return req.Method == "GET" && req.URL.EscapedPath() == "/repos/o/r/releases/tags/releases%2Fv1"
	}, httpmock.JSONResponse(map[string]any{"tag_name": "releases/v1", "published_at": "2026-01-01T00:00:00Z"}))
	client, err := New("github.com", WithClientTransport(registry))
	require.NoError(t, err)
	_, err = client.ReleaseByTag(context.Background(), "o", "r", "releases/v1")
	require.NoError(t, err)
}
