package lockfile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
	"github.com/github/gh-actions-lock/internal/dep"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestState_RelockUpdatesClosure(t *testing.T) {
	for _, unchanged := range []bool{false, true} {
		name := "changed commit drops obsolete children"
		if unchanged {
			name = "same commit retains shared closure"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := LoadState(dir, fakeMetadataResolver{})
			require.NoError(t, err)
			parent := dep.Dependency{NWO: "o/composite", Ref: "main", SHA: strings.Repeat("a", 40)}
			child := dep.Dependency{NWO: "o/old-child", Ref: "v1", SHA: strings.Repeat("b", 40)}
			workflow := ".github/workflows/ci.yml"
			require.NoError(t, store.Set(context.Background(), workflow, []dep.Dependency{parent, child},
				map[string][]string{child.Key(): {parent.Key()}}, map[string]bool{parent.Key(): true}))
			require.NoError(t, store.Save())
			if !unchanged {
				parent.SHA = strings.Repeat("c", 40)
			}
			require.NoError(t, store.Set(context.Background(), workflow, []dep.Dependency{parent}, nil, map[string]bool{parent.Key(): true}))
			require.NoError(t, store.Save())
			reloaded, err := LoadState(dir, nil)
			require.NoError(t, err)
			file := reloaded.File()
			_, childPresent := file.Dependencies[child.Key()]
			assert.Equal(t, unchanged, childPresent)
			if unchanged {
				assert.Equal(t, []string{child.Key()}, file.Dependencies[parent.Key()].Uses)
			} else {
				assert.Empty(t, file.Dependencies[parent.Key()].Uses)
			}
		})
	}
}

func TestState_SetUnionsCanonicalAndRenamedUses(t *testing.T) {
	store, err := LoadState(t.TempDir(), fakeMetadataResolver{})
	require.NoError(t, err)
	sha := strings.Repeat("a", 40)
	for _, key := range []string{"o/first@v1", "o/second@v1"} {
		store.file.Dependencies[key] = parserlock.Action{Ref: "v1", Commit: "sha1-" + sha, OwnerID: 1, RepoID: 2}
	}
	store.file.Dependencies["new/action@v1"] = parserlock.Action{Ref: "v1", Commit: "sha1-" + sha, OwnerID: 1, RepoID: 2, Uses: []string{"o/first@v1"}}
	store.file.Dependencies["old/action@v1"] = parserlock.Action{Ref: "v1", Commit: "sha1-" + sha, Uses: []string{"o/second@v1"}}
	store.file.Dependencies["stale/action@v1"] = parserlock.Action{Ref: "v1", Commit: "sha1-" + strings.Repeat("b", 40), Uses: []string{"o/absent@v1"}}
	d := dep.Dependency{NWO: "new/action", Ref: "v1", SHA: sha, RenamedFrom: []string{"old/action@v1", "stale/action@v1"}}
	require.NoError(t, store.Set(context.Background(), ".github/workflows/ci.yml", []dep.Dependency{d}, nil, nil))
	require.NoError(t, store.Save())
	assert.Equal(t, []string{"o/first@v1", "o/second@v1"}, store.File().Dependencies[d.Key()].Uses)
	assert.NotContains(t, store.File().Dependencies, "old/action@v1")
}

func TestState_RejectsStaleSave(t *testing.T) {
	dir := t.TempDir()
	initial, err := LoadState(dir, fakeMetadataResolver{})
	require.NoError(t, err)
	d := dep.Dependency{NWO: "o/action", Ref: "main", SHA: strings.Repeat("a", 40)}
	require.NoError(t, initial.Set(context.Background(), ".github/workflows/first.yml", []dep.Dependency{d}, nil, nil))
	require.NoError(t, initial.Save())
	first, err := LoadState(dir, fakeMetadataResolver{})
	require.NoError(t, err)
	second, err := LoadState(dir, fakeMetadataResolver{})
	require.NoError(t, err)
	d.SHA = strings.Repeat("b", 40)
	require.NoError(t, first.Set(context.Background(), ".github/workflows/first.yml", []dep.Dependency{d}, nil, nil))
	require.NoError(t, first.Save())
	before, err := os.ReadFile(first.lockPath)
	require.NoError(t, err)
	d.Ref = "v1"
	require.NoError(t, second.Set(context.Background(), ".github/workflows/second.yml", []dep.Dependency{d}, nil, nil))
	assert.ErrorIs(t, second.Save(), ErrStaleLockfile)
	after, err := os.ReadFile(first.lockPath)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestState_MutationGuard(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadState(dir, fakeMetadataResolver{})
	require.NoError(t, err)
	second, err := LoadState(dir, fakeMetadataResolver{})
	require.NoError(t, err)
	release, err := first.BeginMutation(context.Background())
	require.NoError(t, err)
	t.Cleanup(release)
	_, err = second.BeginMutation(context.Background())
	assert.ErrorIs(t, err, ErrConcurrentMutation)
	assert.NoFileExists(t, first.lockPath)
	d := dep.Dependency{NWO: "o/action", Ref: "v1", SHA: strings.Repeat("a", 40)}
	require.NoError(t, first.Set(context.Background(), ".github/workflows/ci.yml", []dep.Dependency{d}, nil, nil))
	require.NoError(t, first.Save(), "Save must reuse the held guard")
	release()
	release()
	_, err = second.BeginMutation(context.Background())
	assert.ErrorIs(t, err, ErrStaleLockfile)
	reloaded, err := LoadState(dir, fakeMetadataResolver{})
	require.NoError(t, err)
	releaseNext, err := reloaded.BeginMutation(context.Background())
	require.NoError(t, err)
	releaseNext()
}

func TestState_MutationRejectsCanceledContext(t *testing.T) {
	store, err := LoadState(t.TempDir(), nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.BeginMutation(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	assert.NoFileExists(t, store.lockPath)
}

func TestState_RecoveryPreservesOriginalUntilSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.lock")
	original := []byte("version: [broken\n")
	require.NoError(t, os.WriteFile(path, original, 0o600))
	store, err := LoadStateForRecoveryAt(path, fakeMetadataResolver{})
	require.NoError(t, err)
	require.NoError(t, store.Validate())
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, after)
	d := dep.Dependency{NWO: "o/action", Ref: "v1", SHA: strings.Repeat("a", 40)}
	require.NoError(t, store.Set(context.Background(), ".github/workflows/ci.yml", []dep.Dependency{d}, nil, nil))
	require.NoError(t, store.Save())
	reloaded, err := LoadStateAt(path, nil)
	require.NoError(t, err)
	assert.Contains(t, reloaded.File().Dependencies, d.Key())
}

func TestState_RejectsMissingMetadata(t *testing.T) {
	for _, transitive := range []bool{false, true} {
		name := "missing workflow dependency"
		if transitive {
			name = "missing child dependency"
		}
		t.Run(name, func(t *testing.T) {
			store, err := LoadState(t.TempDir(), nil)
			require.NoError(t, err)
			store.file.Workflows[".github/workflows/ci.yml"] = []string{"o/action@v1"}
			if transitive {
				store.file.Dependencies["o/action@v1"] = parserlock.Action{Ref: "v1", Commit: "sha1-" + strings.Repeat("a", 40), OwnerID: 1, RepoID: 2, Uses: []string{"o/child@v1"}}
			}
			assert.ErrorContains(t, store.Validate(), "missing dependency")
			assert.ErrorContains(t, store.Save(), "missing dependency")
			assert.NoFileExists(t, store.lockPath)
			body, err := marshalDeterministic(store.file, store.hostname)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(store.lockPath), 0o755))
			require.NoError(t, os.WriteFile(store.lockPath, body, 0o600))
			_, err = LoadStateAt(store.lockPath, nil)
			assert.ErrorIs(t, err, ErrCorruptLockfile)
			if !transitive {
				_, err = store.Get(".github/workflows/ci.yml")
				assert.ErrorContains(t, err, "missing dependency")
			}
		})
	}
}

func TestState_InvalidCandidatePreservesFile(t *testing.T) {
	store, err := LoadState(t.TempDir(), fakeMetadataResolver{})
	require.NoError(t, err)
	d := dep.Dependency{NWO: "o/action", Ref: "v1", SHA: strings.Repeat("a", 40)}
	require.NoError(t, store.Set(context.Background(), ".github/workflows/ci.yml", []dep.Dependency{d}, nil, nil))
	require.NoError(t, store.Save())
	before, err := os.ReadFile(store.lockPath)
	require.NoError(t, err)
	action := store.file.Dependencies[d.Key()]
	action.Uses = []string{d.Key()}
	store.file.Dependencies[d.Key()] = action
	assert.ErrorContains(t, store.Save(), "cycle")
	after, err := os.ReadFile(store.lockPath)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

type transferredMetadata struct{}

func (transferredMetadata) RepoIDs(_ context.Context, _, _, _ string) (int64, int64, error) {
	return 10, 2, nil
}

func TestState_ProximaTransferRefreshesOwnerMetadata(t *testing.T) {
	dir := t.TempDir()
	store, err := LoadState(dir, fakeMetadataResolver{})
	require.NoError(t, err)
	require.NoError(t, store.SetHostname("tenant.ghe.com"))
	d := dep.Dependency{NWO: "old/action", Ref: "v1", SHA: strings.Repeat("a", 40)}
	require.NoError(t, store.Set(context.Background(), ".github/workflows/ci.yml", []dep.Dependency{d}, nil, nil))
	require.NoError(t, store.Save())
	before, err := os.ReadFile(store.lockPath)
	require.NoError(t, err)
	reloaded, err := LoadState(dir, transferredMetadata{})
	require.NoError(t, err)
	require.NoError(t, reloaded.SetHostname("tenant.ghe.com"))
	require.NoError(t, reloaded.VerifyHosts(context.Background()))
	assert.EqualValues(t, 10, reloaded.File().Dependencies[d.Key()].OwnerID)
	assert.EqualValues(t, 2, reloaded.File().Dependencies[d.Key()].RepoID)
	after, err := os.ReadFile(store.lockPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "verification must not write")
	require.NoError(t, reloaded.Set(context.Background(), ".github/workflows/ci.yml", []dep.Dependency{d}, nil, nil))
	assert.EqualValues(t, 10, reloaded.File().Dependencies[d.Key()].OwnerID, "subsequent Set must use refreshed IDs")
}
