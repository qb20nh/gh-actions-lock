package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/github/gh-actions-lock/internal/dep"
	"github.com/github/gh-actions-lock/internal/ghapi"
	"github.com/github/gh-actions-lock/internal/ghapi/httpmock"
	"github.com/github/gh-actions-lock/internal/lockfile"
	"github.com/github/gh-actions-lock/internal/pin"
	"github.com/github/gh-actions-lock/internal/tag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cooldownTestMetadata struct{}

func (cooldownTestMetadata) RepoIDs(context.Context, string, string, string) (int64, int64, error) {
	return 1, 2, nil
}

func TestValidateCooldownCandidates(t *testing.T) {
	sha := strings.Repeat("a", 40)
	oldSHA := strings.Repeat("b", 40)
	old := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	fresh := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	for _, tc := range []struct {
		name        string
		entry       pin.Entry
		prior       []dep.Dependency
		cfg         tag.CooldownConfig
		publication string
		releaseTag  string
		releaseCode int
		commitSHA   string
		wantErr     string
	}{
		{name: "disabled", entry: pin.Entry{NWO: "o/r", Ref: "v1.2.3", SHA: sha, Resolution: pin.Pinned}},
		{name: "zero override", entry: pin.Entry{NWO: "o/r", Ref: "v1.2.3", SHA: sha, Resolution: pin.Pinned}, cfg: tag.CooldownConfig{DefaultDays: 7, RepoOverrides: map[string]int{"O/R": 0}}},
		{name: "old direct release", entry: pin.Entry{NWO: "o/r", Ref: "v1.2.3", SHA: sha, Resolution: pin.Pinned, Direct: true}, cfg: tag.CooldownConfig{DefaultDays: 7}, publication: old, releaseTag: "v1.2.3", commitSHA: sha},
		{name: "fresh direct release", entry: pin.Entry{NWO: "o/r", Ref: "v1.2.3", SHA: sha, Resolution: pin.Pinned, Direct: true}, cfg: tag.CooldownConfig{DefaultDays: 7}, publication: fresh, releaseTag: "v1.2.3", wantErr: "requires at least 7 days"},
		{name: "fresh transitive release", entry: pin.Entry{NWO: "o/r", Ref: "v1", Tag: "v1.2.3", SHA: sha, Resolution: pin.Pinned, RequiredBy: []string{"parent/action@v1"}}, cfg: tag.CooldownConfig{RepoOverrides: map[string]int{"o/r": 7}}, publication: fresh, releaseTag: "v1.2.3", wantErr: "requires at least 7 days"},
		{name: "verified resolution cannot bypass", entry: pin.Entry{NWO: "o/r", Ref: "v1.2.3", SHA: sha, Resolution: pin.Verified}, cfg: tag.CooldownConfig{DefaultDays: 7}, publication: fresh, releaseTag: "v1.2.3", wantErr: "requires at least 7 days"},
		{name: "unchanged SHA under new ref", entry: pin.Entry{NWO: "O/R", Ref: "v1.2.3", SHA: sha, Resolution: pin.Pinned}, prior: []dep.Dependency{{NWO: "o/r", Ref: "v1", SHA: sha}}, cfg: tag.CooldownConfig{DefaultDays: 7}},
		{name: "unchanged repository rename", entry: pin.Entry{NWO: "new/r", Ref: "v1.2.3", SHA: sha, Resolution: pin.Pinned, RenamedFrom: []string{"o/r@v1"}}, prior: []dep.Dependency{{NWO: "o/r", Ref: "v1", SHA: sha}}, cfg: tag.CooldownConfig{DefaultDays: 7}},
		{name: "changed SHA on new exact release", entry: pin.Entry{NWO: "o/r", Ref: "v1.2.3", SHA: sha, Resolution: pin.Pinned}, prior: []dep.Dependency{{NWO: "o/r", Ref: "v1", Tag: "v1.2.2", SHA: oldSHA}}, cfg: tag.CooldownConfig{DefaultDays: 7}, publication: old, releaseTag: "v1.2.3", commitSHA: sha},
		{name: "recorded release retarget", entry: pin.Entry{NWO: "o/r", Ref: "v1.2.3", SHA: sha, Resolution: pin.Pinned}, prior: []dep.Dependency{{NWO: "o/r", Ref: "v1.2.3", SHA: oldSHA}}, cfg: tag.CooldownConfig{DefaultDays: 7}, wantErr: "was retargeted from a recorded commit"},
		{name: "explicit full ref wins over discovered tag", entry: pin.Entry{NWO: "o/r", Ref: "v1.2.3", Tag: "v1.2.2", SHA: sha, Resolution: pin.Pinned}, cfg: tag.CooldownConfig{DefaultDays: 7}, publication: fresh, releaseTag: "v1.2.3", wantErr: "requires at least 7 days"},
		{name: "missing publication", entry: pin.Entry{NWO: "o/r", Ref: "v1.2.3", SHA: sha, Resolution: pin.Pinned}, cfg: tag.CooldownConfig{DefaultDays: 7}, releaseTag: "v1.2.3", wantErr: "no published release date"},
		{name: "invalid publication", entry: pin.Entry{NWO: "o/r", Ref: "v1.2.3", SHA: sha, Resolution: pin.Pinned}, cfg: tag.CooldownConfig{DefaultDays: 7}, publication: "bad-date", releaseTag: "v1.2.3", wantErr: "invalid published_at"},
		{name: "API failure", entry: pin.Entry{NWO: "o/r", Ref: "v1.2.3", SHA: sha, Resolution: pin.Pinned}, cfg: tag.CooldownConfig{DefaultDays: 7}, releaseTag: "v1.2.3", releaseCode: 500, wantErr: "release age is unknown"},
		{name: "missing exact release", entry: pin.Entry{NWO: "o/r", Ref: "v1.2.3", SHA: sha, Resolution: pin.Pinned}, cfg: tag.CooldownConfig{DefaultDays: 7}, releaseTag: "v1.2.3", releaseCode: 404, wantErr: "release age is unknown"},
		{name: "release tag targets another commit", entry: pin.Entry{NWO: "o/r", Ref: "v1.2.3", SHA: sha, Resolution: pin.Pinned}, cfg: tag.CooldownConfig{DefaultDays: 7}, publication: old, releaseTag: "v1.2.3", commitSHA: oldSHA, wantErr: "not candidate"},
		{name: "unknown branch age", entry: pin.Entry{NWO: "o/r", Ref: "main", SHA: sha, Resolution: pin.Pinned}, cfg: tag.CooldownConfig{DefaultDays: 7}, wantErr: "no exact release tag"},
		{name: "unknown bare SHA age", entry: pin.Entry{NWO: "o/r", Ref: sha, SHA: sha, Resolution: pin.Pinned}, cfg: tag.CooldownConfig{DefaultDays: 7}, wantErr: "no exact release tag"},
		{name: "unknown mutable alias age", entry: pin.Entry{NWO: "o/r", Ref: "v1", SHA: sha, Resolution: pin.Pinned}, cfg: tag.CooldownConfig{DefaultDays: 7}, wantErr: "no exact release tag"},
		{name: "bare SHA with discovered exact tag", entry: pin.Entry{NWO: "o/r", Ref: sha, Tag: "v1.2.3", SHA: sha, Resolution: pin.Pinned}, cfg: tag.CooldownConfig{DefaultDays: 7}, publication: old, releaseTag: "v1.2.3", commitSHA: sha},
		{name: "same SHA on different host is new", entry: pin.Entry{Hostname: "other.example.com", NWO: "o/r", Ref: "v1.2.3", SHA: sha, Resolution: pin.Pinned}, prior: []dep.Dependency{{NWO: "o/r", Ref: "v1.2.3", SHA: sha}}, cfg: tag.CooldownConfig{DefaultDays: 7}, wantErr: "expected other.example.com"},
		{name: "unresolved is already blocked", entry: pin.Entry{NWO: "o/r", Ref: "v1.2.3", Resolution: pin.Unresolved}, cfg: tag.CooldownConfig{DefaultDays: 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := &httpmock.Registry{}
			t.Cleanup(func() { registry.Verify(t) })
			if tc.releaseTag != "" {
				response := httpmock.JSONResponse(map[string]any{"tag_name": tc.releaseTag, "published_at": tc.publication})
				if tc.releaseCode != 0 {
					response = httpmock.StatusResponse(tc.releaseCode)
				}
				registry.Register(httpmock.REST("GET", `^/repos/[oO]/[rR]/releases/tags/`+tc.releaseTag+`$`), response)
			}
			if tc.commitSHA != "" {
				registry.Register(httpmock.REST("GET", `^/repos/[oO]/[rR]/commits/refs/tags/`+tc.releaseTag+`$`), httpmock.JSONResponse(map[string]any{"sha": tc.commitSHA}))
			}
			client, err := ghapi.New("github.com", ghapi.WithClientTransport(registry))
			require.NoError(t, err)
			store, err := lockfile.LoadState(t.TempDir(), cooldownTestMetadata{})
			require.NoError(t, err)
			if len(tc.prior) > 0 {
				require.NoError(t, store.Set(context.Background(), ".github/workflows/ci.yml", tc.prior, nil, nil))
			}
			before := store.File()
			record := &pin.Record{Entries: []pin.Entry{tc.entry}}
			err = validateCooldownCandidates(context.Background(), record, store, client, tc.cfg)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, before, store.File(), "validation must not mutate the original store")
			assert.Equal(t, tc.entry, record.Entries[0], "validation must not substitute another commit")
		})
	}
}

func TestValidateCooldownPublication(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		age     time.Duration
		wantErr bool
	}{
		{name: "one second too new", age: 7*24*time.Hour - time.Second, wantErr: true},
		{name: "exact boundary", age: 7 * 24 * time.Hour},
		{name: "older", age: 30 * 24 * time.Hour},
		{name: "future publication", age: -time.Second, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCooldownPublication(now.Add(-tc.age).Format(time.RFC3339), 7, now)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	assert.Error(t, validateCooldownPublication(now.Add(-30*24*time.Hour).Format(time.RFC3339), int(^uint(0)>>1), now), "large day counts must not overflow into eligibility")
}

func TestRun_CooldownWithNoNarrow(t *testing.T) {
	for _, tc := range []struct {
		name string
		age  time.Duration
		pass bool
	}{
		{name: "young release writes nothing", age: 24 * time.Hour},
		{name: "eligible release commits", age: 30 * 24 * time.Hour, pass: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := &httpmock.Registry{}
			t.Cleanup(func() { registry.Verify(t) })
			registry.Register(httpmock.GraphQL(`commit: object\(oid`), parityOK)
			locked := writeTempWorkflow(t, `name: ci
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4.2.1
`, "actions/checkout@v4.2.1=sha1-"+parityTestSHA)
			registry.Register(httpmock.GraphQLForRepo("actions", "setup-go"), httpmock.JSONResponse(map[string]any{
				"data": map[string]any{"a0": testRepoResponse("actions/setup-go", parityTestMoved, nodeActionYAML)},
			}))
			require.NoError(t, os.MkdirAll(".git", 0o755))
			local := filepath.Join(".github", "actions", "local", "action.yml")
			require.NoError(t, os.MkdirAll(filepath.Dir(local), 0o755))
			require.NoError(t, os.WriteFile(local, []byte("runs:\n  using: composite\n  steps:\n    - run: echo hi\n      shell: bash\n"), 0o600))
			clean := filepath.Join(".github", "workflows", "clean.yml")
			require.NoError(t, os.WriteFile(clean, []byte(cleanWorkflow+"      - uses: ./.github/actions/local\n"), 0o600))
			policy := "version: 2\nupdates:\n  - package-ecosystem: github-actions\n    directory: /\n    schedule:\n      interval: weekly\n    cooldown:\n      default-days: 7\n"
			require.NoError(t, os.WriteFile(filepath.Join(".github", "dependabot.yml"), []byte(policy), 0o600))
			registry.Register(httpmock.REST("GET", `^/repos/actions/setup-go/releases/tags/v5\.0\.0$`),
				httpmock.JSONResponse(map[string]any{"tag_name": "v5.0.0", "published_at": time.Now().Add(-tc.age).Format(time.RFC3339)}))
			if tc.pass {
				registry.Register(httpmock.REST("GET", `^/repos/actions/setup-go/commits/refs/tags/v5\.0\.0$`),
					httpmock.JSONResponse(map[string]any{"sha": parityTestMoved}))
				registry.Register(httpmock.REST("GET", `^/repos/actions/setup-go$`), httpmock.JSONResponse(map[string]any{
					"default_branch": "main", "id": 2, "owner": map[string]any{"id": 1},
				}))
			}
			before := snapshotWorkflows(t)

			_, stderr, err := runCommandWithHTTP(t, registry, "--no-narrow", "--migrate-local-actions", locked, clean)
			if !tc.pass {
				require.Error(t, err, stderr)
				assert.Contains(t, err.Error(), "requires at least 7 days")
				assert.Equal(t, before, snapshotWorkflows(t), "the age gate must reject before source or lockfile writes")
				return
			}
			require.NoError(t, err, stderr)
			assert.Contains(t, readTempLockfilePins(t), "actions/setup-go@v5.0.0")
			assert.Contains(t, readTempLockfilePins(t), parityTestMoved)
			source, err := os.ReadFile(clean)
			require.NoError(t, err)
			assert.Contains(t, string(source), "$/.github/actions/local")
		})
	}
}
