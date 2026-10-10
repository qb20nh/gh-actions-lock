package ghapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/github/gh-actions-lock/internal/ghapi/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReusableWorkflowMetadataExactPath(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const content = "on: workflow_call\njobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4\n"
	for _, ext := range []string{"yml", "yaml"} {
		t.Run(ext, func(t *testing.T) {
			filePath := ".github/workflows/reusable." + ext
			registry := &httpmock.Registry{}
			defer registry.Verify(t)
			registry.Register(httpmock.GraphQL(`file: file`), httpmock.GraphQLQuery(
				fmt.Sprintf(`{"data":{"a0":{"object":{"oid":%q,"file":{"mode":33188,"object":{"text":%q}}}}}}`, sha, content),
				func(query string, variables map[string]any) {
					assert.Equal(t, filePath, variables["yml0"])
					assert.Equal(t, filePath, variables["yaml0"])
					assert.Equal(t, "v1^{commit}", variables["expr0"])
					assert.Contains(t, query, "mode object")
					assert.Contains(t, query, "text isTruncated isBinary")
				}))
			client, err := New("github.com", WithClientTransport(registry))
			require.NoError(t, err)
			results := client.ResolveActionFiles(context.Background(), []ActionFileRequest{{Owner: "o", Repo: "r", Path: filePath, Ref: "v1"}})
			require.Len(t, results, 1)
			require.NoError(t, results[0].Err)
			assert.Equal(t, content, results[0].ActionYML)
			assert.Equal(t, sha, results[0].CommitOID)
		})
	}
}

func TestGraphQLMetadataFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		filePath string
		file     any
		want     string
	}{
		{name: "missing action", want: "metadata action.yml not found"},
		{name: "missing workflow", filePath: ".github/workflows/ci.yml", want: "metadata .github/workflows/ci.yml not found"},
		{name: "directory", file: map[string]any{"mode": 0o040000, "object": map[string]any{}}, want: "YAML mapping"},
		{name: "invalid yaml", file: map[string]any{"object": map[string]any{"text": "runs: ["}}, want: "parsing metadata"},
		{name: "empty file", file: map[string]any{"object": map[string]any{"text": ""}}, want: "YAML mapping"},
		{name: "truncated valid yaml", file: map[string]any{"object": map[string]any{"text": "runs:\n  using: composite\n", "isTruncated": true}}, want: "is truncated"},
		{name: "binary file", file: map[string]any{"object": map[string]any{"isBinary": true}}, want: "is binary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return jsonHTTP(map[string]any{"data": map[string]any{"a0": map[string]any{"object": map[string]any{"oid": "immutable", "file": tc.file}}}})
			})
			client, err := New("github.com", WithClientTransport(transport))
			require.NoError(t, err)
			results := client.ResolveActionFiles(context.Background(), []ActionFileRequest{{Owner: "o", Repo: "r", Path: tc.filePath, Ref: "v1"}})
			require.Len(t, results, 1)
			require.ErrorContains(t, results[0].Err, tc.want)
			assert.True(t, IsMetadataError(fmt.Errorf("resolving definition: %w", results[0].Err)))
		})
	}
}

func TestGraphQLMetadataSymlinks(t *testing.T) {
	const content = "runs:\n  using: composite\n  steps:\n    - uses: vendor/child@v1\n"
	for _, tc := range []struct {
		name       string
		filePath   string
		target     string
		links      map[string]string
		missing    bool
		apiMissing bool
		truncated  bool
		wantErr    string
	}{
		{name: "relative target", target: "definitions/action.yml"},
		{name: "parent path within repository", filePath: "actions/tool", target: "../../definitions/action.yml"},
		{name: "workflow target", filePath: ".github/workflows/ci.yml", target: "definitions/ci.yml"},
		{name: "link chain", target: "first.yml", links: map[string]string{"first.yml": "second.yml"}},
		{name: "cycle", target: "first.yml", links: map[string]string{"first.yml": "action.yml"}, wantErr: "symlink cycle"},
		{name: "escape", target: "../outside.yml", wantErr: "outside repository"},
		{name: "absolute", target: "/outside.yml", wantErr: "invalid target"},
		{name: "missing target", target: "missing.yml", missing: true, wantErr: "missing or is not a file"},
		{name: "missing target API verdict", target: "missing.yml", apiMissing: true, wantErr: "target missing.yml not found"},
		{name: "depth", target: "next/target.yml", links: map[string]string{"*": "next/target.yml"}, wantErr: "symlink depth"},
		{name: "truncated target", target: "actual.yml", truncated: true, wantErr: "is truncated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				var payload struct {
					Variables map[string]any `json:"variables"`
				}
				require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
				if _, initial := payload.Variables["owner0"]; initial {
					return jsonHTTP(map[string]any{"data": map[string]any{"a0": map[string]any{"object": map[string]any{"oid": "immutable", "file": map[string]any{"mode": 0o120000, "object": map[string]any{"text": tc.target}}}}}})
				}
				assert.Equal(t, "immutable", payload.Variables["sha"])
				assert.Equal(t, "o", payload.Variables["owner"])
				filePath := payload.Variables["path"].(string)
				if tc.apiMissing {
					return jsonHTTP(map[string]any{"data": map[string]any{"repository": nil}, "errors": []any{map[string]any{"type": "NOT_FOUND", "path": []string{"repository", "object", "file"}, "message": "missing file"}}})
				}
				var file any
				if !tc.missing {
					mode, text := 0o100644, content
					if target, ok := tc.links[filePath]; ok {
						mode, text = 0o120000, target
					} else if target, ok := tc.links["*"]; ok {
						mode, text = 0o120000, target
					}
					file = map[string]any{"mode": mode, "object": map[string]any{"text": text, "isTruncated": tc.truncated}}
				}
				return jsonHTTP(map[string]any{"data": map[string]any{"repository": map[string]any{"object": map[string]any{"file": file}}}})
			})
			client, err := New("github.com", WithClientTransport(transport))
			require.NoError(t, err)
			results := client.ResolveActionFiles(context.Background(), []ActionFileRequest{{Owner: "o", Repo: "r", Path: tc.filePath, Ref: "mutable"}})
			require.Len(t, results, 1)
			if tc.wantErr != "" {
				require.ErrorContains(t, results[0].Err, tc.wantErr)
				assert.True(t, IsMetadataError(results[0].Err))
				return
			}
			require.NoError(t, results[0].Err)
			assert.Equal(t, content, results[0].ActionYML)
		})
	}
}

func TestRESTMetadataPathsAndFailures(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const content = "runs:\n  using: composite\n  steps:\n    - uses: vendor/child@v1\n"
	for _, tc := range []struct {
		name       string
		filePath   string
		files      map[string]string
		statuses   map[string]int
		wantErr    string
		apiFailure bool
	}{
		{name: "workflow yml", filePath: ".github/workflows/ci.yml", files: map[string]string{".github/workflows/ci.yml": content}},
		{name: "workflow yaml", filePath: ".github/workflows/ci.yaml", files: map[string]string{".github/workflows/ci.yaml": content}},
		{name: "action yaml fallback", filePath: "tool", files: map[string]string{"tool/action.yaml": content}},
		{name: "dereferenced file symlink", files: map[string]string{"action.yml": content}},
		{name: "missing action", wantErr: "action metadata action.yml or action.yaml could not be read"},
		{name: "missing workflow", filePath: ".github/workflows/ci.yml", wantErr: "workflow metadata .github/workflows/ci.yml not found"},
		{name: "malformed yaml", files: map[string]string{"action.yml": "runs: ["}, wantErr: "parsing metadata"},
		{name: "unresolved symlink", files: map[string]string{"action.yml": `{"type":"symlink","target":"../outside.yml"}`}, wantErr: "missing or out-of-repository target"},
		{name: "directory", files: map[string]string{"action.yml": `[{"type":"file"}]`}, wantErr: "YAML mapping"},
		{name: "API failure", statuses: map[string]int{"action.yml": 500}, wantErr: "fetching action.yml", apiFailure: true},
		{name: "fallback API failure", statuses: map[string]int{"action.yaml": 500}, wantErr: "could not be read", apiFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requested []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/repos/o/r":
					fmt.Fprint(w, `{"full_name":"o/r"}`)
				case "/repos/o/r/commits/v1":
					fmt.Fprintf(w, `{"sha":%q}`, sha)
				default:
					filePath := strings.TrimPrefix(req.URL.Path, "/repos/o/r/contents/")
					requested = append(requested, filePath)
					assert.Equal(t, sha, req.URL.Query().Get("ref"))
					if status := tc.statuses[filePath]; status != 0 {
						w.WriteHeader(status)
						return
					}
					if text, ok := tc.files[filePath]; ok {
						fmt.Fprint(w, text)
					} else {
						http.NotFound(w, req)
					}
				}
			}))
			defer server.Close()
			client := &Client{Hostname: "github.com", restOnly: true, anonBaseURL: server.URL, anonHTTP: server.Client()}
			results := client.ResolveActionFiles(context.Background(), []ActionFileRequest{{Owner: "o", Repo: "r", Path: tc.filePath, Ref: "v1"}})
			require.Len(t, results, 1)
			if IsReusableWorkflowPath(tc.filePath) {
				assert.Equal(t, []string{tc.filePath}, requested)
			}
			if tc.wantErr != "" {
				require.ErrorContains(t, results[0].Err, tc.wantErr)
				assert.Equal(t, !tc.apiFailure, IsMetadataError(results[0].Err))
				return
			}
			require.NoError(t, results[0].Err)
			assert.Equal(t, content, results[0].ActionYML)
			assert.Equal(t, sha, results[0].CommitOID)
		})
	}
}

func TestMetadataTransportFailuresAreNotInvalidDefinitions(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%v", symlink), func(t *testing.T) {
			failure := errors.New("transport unavailable")
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				var payload struct {
					Variables map[string]any `json:"variables"`
				}
				require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
				if _, initial := payload.Variables["owner0"]; initial && symlink {
					return jsonHTTP(map[string]any{"data": map[string]any{"a0": map[string]any{"object": map[string]any{"oid": "immutable", "file": map[string]any{"mode": 0o120000, "object": map[string]any{"text": "actual.yml"}}}}}})
				}
				return nil, failure
			})
			client, err := New("github.com", WithClientTransport(transport))
			require.NoError(t, err)
			results := client.ResolveActionFiles(context.Background(), []ActionFileRequest{{Owner: "o", Repo: "r", Ref: "v1"}})
			require.Len(t, results, 1)
			require.ErrorIs(t, results[0].Err, failure)
			assert.False(t, IsMetadataError(results[0].Err))
		})
	}
}
