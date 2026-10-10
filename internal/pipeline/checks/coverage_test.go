package checks

import (
	"context"
	"strings"
	"testing"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
	"github.com/stretchr/testify/assert"
)

func TestRunChecksCoverage(t *testing.T) {
	const path = ".github/workflows/ci.yml"
	for _, tc := range []struct {
		name string
		ref  string
		pins []string
		deps map[string]parserlock.Action
		want []string
	}{
		{
			name: "unrecorded SHA is missing coverage",
			ref:  shaCheckoutV4,
			want: []string{string(NotPinned), string(ShaAsRef)},
		},
		{
			name: "symbolic pin covers the same SHA",
			ref:  shaCheckoutV4,
			pins: []string{"actions/checkout@v4"},
			deps: map[string]parserlock.Action{"actions/checkout@v4": {Commit: "sha1-" + shaCheckoutV4}},
			want: []string{string(ShaAsRef)},
		},
		{
			name: "SHA comparison ignores hex case",
			ref:  strings.ToUpper(shaCheckoutV4),
			pins: []string{"actions/checkout@v4"},
			deps: map[string]parserlock.Action{"actions/checkout@v4": {Commit: "sha1-" + shaCheckoutV4}},
			want: []string{string(ShaAsRef)},
		},
		{
			name: "different SHA does not cover the workflow",
			ref:  shaCheckoutV3,
			pins: []string{"actions/checkout@v4"},
			deps: map[string]parserlock.Action{"actions/checkout@v4": {Commit: "sha1-" + shaCheckoutV4}},
			want: []string{string(NotPinned), string(ShaAsRef), string(Stale)},
		},
		{
			name: "direct pin without metadata is uncovered",
			ref:  "v4",
			pins: []string{"actions/checkout@v4"},
			want: []string{string(NotPinned)},
		},
		{
			name: "direct pin without a commit is uncovered",
			ref:  "v4",
			pins: []string{"actions/checkout@v4"},
			deps: map[string]parserlock.Action{"actions/checkout@v4": {}},
			want: []string{string(NotPinned)},
		},
		{
			name: "missing nested metadata is incomplete",
			ref:  "v4",
			pins: []string{"actions/checkout@v4"},
			deps: map[string]parserlock.Action{"actions/checkout@v4": {Commit: "sha1-" + shaCheckoutV4, Uses: []string{"actions/setup-go@v5"}}},
			want: []string{string(NotPinned)},
		},
		{
			name: "complete nested metadata passes",
			ref:  "v4",
			pins: []string{"actions/checkout@v4"},
			deps: map[string]parserlock.Action{
				"actions/checkout@v4": {Commit: "sha1-" + shaCheckoutV4, Uses: []string{"actions/setup-go@v5"}},
				"actions/setup-go@v5": {Commit: "sha1-" + shaSetupGoV5},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lf := parserlock.File{Workflows: map[string][]string{path: tc.pins}, Dependencies: tc.deps}
			findings := RunChecks(context.Background(), checkParsedWF(path, checkRef("actions", "checkout", tc.ref)), lf, nil)
			assert.ElementsMatch(t, tc.want, findingCategories(findings))
			for _, finding := range findings {
				if finding.Category == NotPinned {
					assert.Equal(t, SeverityError, finding.Severity)
					assert.False(t, finding.IsValid())
				}
			}
		})
	}
}

func TestRunChecksMissingChildNamesParent(t *testing.T) {
	const path = ".github/workflows/ci.yml"
	lf := parserlock.File{
		Workflows: map[string][]string{path: {"octo/composite@v1"}},
		Dependencies: map[string]parserlock.Action{
			"octo/composite@v1": {Commit: "sha1-" + shaCheckoutV4, Uses: []string{"actions/setup-go@v5"}},
		},
	}
	findings := RunChecks(context.Background(), checkParsedWF(path, checkRef("octo", "composite", "v1")), lf, nil)
	if assert.Len(t, findings, 1) {
		assert.Equal(t, "octo/composite@v1", findings[0].ParentNWO)
		assert.Contains(t, findings[0].Detail, "actions/setup-go@v5")
	}
}
