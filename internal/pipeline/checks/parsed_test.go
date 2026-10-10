package checks

import (
	"strings"
	"testing"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
	"github.com/github/gh-actions-lock/internal/dep"
	"github.com/stretchr/testify/assert"
)

func TestPartitionRefsRequiresCommitMetadata(t *testing.T) {
	pw := ParsedWorkflow{
		Refs:         []parserlock.ActionRef{{Owner: "actions", Repo: "checkout", Ref: "v4"}},
		ExistingDeps: []dep.Dependency{{NWO: "actions/checkout", Ref: "v4"}},
	}
	recorded, unrecorded := pw.PartitionRefs()
	assert.Empty(t, recorded)
	assert.Len(t, unrecorded, 1)
	assert.Empty(t, pw.RecordedDeps(pw.Refs))
}

func TestPartitionRefsMatchesUppercaseSHA(t *testing.T) {
	const sha = "de0fac2e4500dabe0009e67214ff5f5447ce83dd"
	pw := ParsedWorkflow{
		Refs:         []parserlock.ActionRef{{Owner: "ACTIONS", Repo: "Checkout", Ref: strings.ToUpper(sha)}},
		ExistingDeps: []dep.Dependency{{NWO: "actions/checkout", Ref: "v6", SHA: sha}},
	}
	recorded, unrecorded := pw.PartitionRefs()
	assert.Len(t, recorded, 1)
	assert.Empty(t, unrecorded)
	assert.Len(t, pw.RecordedDeps(recorded), 1)
}

func TestIsImmutableRef(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"v4.2.1", true},      // full semver: one commit for life
		{"4.2.1", true},       // full semver without leading v
		{"v4", false},         // major-only: moves as patches land
		{"v4.2", false},       // major.minor: moves
		{"v4.2.1-rc1", false}, // prerelease: not a full stable tag
		{"main", false},       // branch
		{"master", false},     // branch
		{"de0fac2e4500dabe0009e67214ff5f5447ce83dd", false}, // bare SHA
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			assert.Equal(t, tt.want, IsImmutableRef(tt.ref))
		})
	}
}
