package workflowfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubpathRewriteLookup(t *testing.T) {
	replacements := map[string]string{
		"actions/cache@27d5ce7": "actions/cache@v5.0.5",
	}

	tests := []struct {
		name   string
		input  string
		want   string
		wantOK bool
	}{
		{
			name:   "sub-path restore",
			input:  "actions/cache/restore@27d5ce7",
			want:   "actions/cache/restore@v5.0.5",
			wantOK: true,
		},
		{
			name:   "sub-path save",
			input:  "actions/cache/save@27d5ce7",
			want:   "actions/cache/save@v5.0.5",
			wantOK: true,
		},
		{
			name:   "no sub-path (exact match, not handled here)",
			input:  "actions/cache@27d5ce7",
			want:   "",
			wantOK: false,
		},
		{
			name:   "no match in replacements",
			input:  "actions/checkout/sub@abc1234",
			want:   "",
			wantOK: false,
		},
		{
			name:   "no @ separator",
			input:  "actions/cache/restore",
			want:   "",
			wantOK: false,
		},
		{
			name:   "deeply nested sub-path",
			input:  "actions/cache/restore/deep/nested@27d5ce7",
			want:   "actions/cache/restore/deep/nested@v5.0.5",
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := subpathRewriteLookup(tt.input, replacements)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestRewriteActionRefs(t *testing.T) {
	f, err := Load("testdata/simple.yml")
	require.NoError(t, err)

	output, changed, err := f.RewriteActionRefs(map[string]string{
		"actions/checkout@v4": "actions/checkout@v5",
		"actions/setup-go@v5": "actions/setup-go@v6",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, changed)

	s := string(output)
	assert.Contains(t, s, "uses: actions/checkout@v5")
	assert.Contains(t, s, "uses: actions/setup-go@v6")
	assert.NotContains(t, s, "uses: actions/checkout@v4")
	assert.NotContains(t, s, "uses: actions/setup-go@v5")
}

func TestRewriteActionRefs_DropsTrailingComments(t *testing.T) {
	content := []byte(`name: ci
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4 # pinned for stability
      - uses: actions/setup-go@v5
`)
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "with_comment.yml")
	require.NoError(t, os.WriteFile(path, content, 0o644))

	f, err := Load(path)
	require.NoError(t, err)

	output, changed, err := f.RewriteActionRefs(map[string]string{
		"actions/checkout@v4": "actions/checkout@v4.2.1",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, changed)

	s := string(output)
	assert.Contains(t, s, "uses: actions/checkout@v4.2.1\n")
	assert.NotContains(t, s, "pinned for stability")
}

func TestRewriteActionRefs_OnlyMatchesYAMLUses(t *testing.T) {
	content := []byte(`name: ci
on: push
# DO NOT USE actions/checkout@v4 - see docs
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
`)
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "comment_first.yml")
	require.NoError(t, os.WriteFile(path, content, 0o644))

	f, err := Load(path)
	require.NoError(t, err)

	output, changed, err := f.RewriteActionRefs(map[string]string{
		"actions/checkout@v4": "actions/checkout@v4.2.1",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, changed)

	s := string(output)
	assert.Contains(t, s, "uses: actions/checkout@v4.2.1")
	assert.Contains(t, s, "# DO NOT USE actions/checkout@v4 - see docs")
}

func TestRewriteActionRefs_AnchoredAtColumn(t *testing.T) {
	content := []byte("jobs:\n  a:\n    steps:\n      # bumped from actions/checkout@v3 — do not revert\n      - uses: actions/checkout@v3\n")
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "anchor.yml")
	require.NoError(t, os.WriteFile(path, content, 0o644))

	f, err := Load(path)
	require.NoError(t, err)

	output, changed, err := f.RewriteActionRefs(map[string]string{
		"actions/checkout@v3": "actions/checkout@v4",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, changed)

	s := string(output)
	assert.Contains(t, s, "      - uses: actions/checkout@v4\n")
	assert.Contains(t, s, "# bumped from actions/checkout@v3 — do not revert")
}

func TestRewriteActionRefs_RejectsAnchorsAndAliases(t *testing.T) {
	content := []byte("jobs:\n  a:\n    steps:\n      - uses: &pinned actions/checkout@v3\n  b:\n    steps:\n      - uses: *pinned\n")
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "anchored.yml")
	require.NoError(t, os.WriteFile(path, content, 0o644))

	f, err := Load(path)
	require.NoError(t, err)

	output, changed, err := f.RewriteActionRefs(map[string]string{
		"actions/checkout@v3": "actions/checkout@v4",
	})
	require.ErrorContains(t, err, "anchored or aliased")
	assert.Equal(t, 0, changed)
	assert.Equal(t, content, output)
}

func TestRewriteActionRefs_SubPathActions(t *testing.T) {
	content := []byte(`jobs:
  build:
    steps:
      - uses: actions/cache/restore@27d5ce7 # restore cache
      - uses: actions/cache/save@27d5ce7
      - uses: actions/cache@27d5ce7
      - uses: actions/checkout@v3
`)
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "subpath.yml")
	require.NoError(t, os.WriteFile(path, content, 0o644))

	f, err := Load(path)
	require.NoError(t, err)

	output, changed, err := f.RewriteActionRefs(map[string]string{
		"actions/cache@27d5ce7": "actions/cache@v5.0.5",
	})
	require.NoError(t, err)
	assert.Equal(t, 3, changed)

	out := string(output)
	assert.Contains(t, out, "actions/cache/restore@v5.0.5\n")
	assert.Contains(t, out, "actions/cache/save@v5.0.5")
	assert.Contains(t, out, "actions/cache@v5.0.5")
	assert.Contains(t, out, "actions/checkout@v3")
}

func TestRewriteActionRefs_PreservesOtherYAMLData(t *testing.T) {
	content := []byte(`env:
  uses: actions/checkout@v4
jobs:
  call:
    uses: owner/workflow/.github/workflows/test.yml@v1
    with:
      uses: actions/checkout@v4
  test:
    steps:
      - {name: 한글, uses: actions/checkout@v4, with: {uses: actions/checkout@v4}}
      - {uses: actions/checkout@v4, name: 'label # stable'}
`)
	f, err := Parse("ci.yml", content)
	require.NoError(t, err)

	output, changed, err := f.RewriteActionRefs(map[string]string{
		"actions/checkout@v4":                          "actions/checkout@v4.2.2",
		"owner/workflow/.github/workflows/test.yml@v1": "new-owner/workflow/.github/workflows/test.yml@v1",
	})
	require.NoError(t, err)
	assert.Equal(t, 3, changed)
	assert.Contains(t, string(output), "env:\n  uses: actions/checkout@v4\n")
	assert.Contains(t, string(output), "with:\n      uses: actions/checkout@v4\n")
	assert.Contains(t, string(output), "{name: 한글, uses: actions/checkout@v4.2.2, with: {uses: actions/checkout@v4}}")
	assert.Contains(t, string(output), "{uses: actions/checkout@v4.2.2, name: 'label # stable'}")
	assert.Contains(t, string(output), "uses: new-owner/workflow/.github/workflows/test.yml@v1")
	_, err = Parse("ci.yml", output)
	require.NoError(t, err)
}

func TestRewriteActionRefs_SourceSpans(t *testing.T) {
	tests := []struct {
		name, content, want string
	}{
		{
			name:    "multiple uses on one line",
			content: "jobs: {a: {steps: [{uses: actions/checkout@v4}, {uses: actions/setup-go@v5}]}}\n",
			want:    "jobs: {a: {steps: [{uses: actions/checkout@v4.2.2}, {uses: actions/setup-go@v5.1.0}]}}\n",
		},
		{
			name:    "single quoted value",
			content: "jobs:\n  a:\n    steps:\n      - uses: 'actions/checkout@v4' # old comment\n",
			want:    "jobs:\n  a:\n    steps:\n      - uses: 'actions/checkout@v4.2.2'\n",
		},
		{
			name:    "escaped double quoted value",
			content: "jobs:\n  a:\n    steps:\n      - uses: \"actions/\\u0063heckout@v4\"\n",
			want:    "jobs:\n  a:\n    steps:\n      - uses: \"actions/checkout@v4.2.2\"\n",
		},
		{
			name:    "CRLF comment",
			content: "jobs:\r\n  a:\r\n    steps:\r\n      - uses: actions/checkout@v4 # old comment\r\n",
			want:    "jobs:\r\n  a:\r\n    steps:\r\n      - uses: actions/checkout@v4.2.2\r\n",
		},
		{
			name:    "composite action dependency",
			content: "inputs:\n  uses:\n    default: actions/checkout@v4\nruns:\n  using: composite\n  steps:\n    - uses: actions/checkout@v4\n      with: {uses: actions/checkout@v4}\n",
			want:    "inputs:\n  uses:\n    default: actions/checkout@v4\nruns:\n  using: composite\n  steps:\n    - uses: actions/checkout@v4.2.2\n      with: {uses: actions/checkout@v4}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := Parse("ci.yml", []byte(tt.content))
			require.NoError(t, err)
			output, changed, err := f.RewriteActionRefs(map[string]string{
				"actions/checkout@v4": "actions/checkout@v4.2.2",
				"actions/setup-go@v5": "actions/setup-go@v5.1.0",
			})
			require.NoError(t, err)
			assert.Positive(t, changed)
			assert.Equal(t, tt.want, string(output))
		})
	}
}

func TestRewriteActionRefs_RejectsUnsupportedScalarsBeforeAnyChanges(t *testing.T) {
	tests := []struct {
		name, uses, wantError string
	}{
		{"folded", ">-\n          actions/checkout@v4", "multiline"},
		{"literal", "|-\n          actions/checkout@v4", "multiline"},
		{"anchored scalar", "&action actions/checkout@v4", "anchored or aliased"},
		{"aliased scalar", "*action", "anchored or aliased"},
		{"explicit tag", "!!str actions/checkout@v4", "unsupported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := []byte("env:\n  action: &action actions/checkout@v4\njobs:\n  a:\n    steps:\n      - uses: actions/setup-go@v5\n      - uses: " + tt.uses + "\n")
			f, err := Parse("ci.yml", content)
			require.NoError(t, err)
			output, changed, err := f.RewriteActionRefs(map[string]string{
				"actions/checkout@v4": "actions/checkout@v4.2.2",
				"actions/setup-go@v5": "actions/setup-go@v5.1.0",
			})
			require.ErrorContains(t, err, tt.wantError)
			assert.Zero(t, changed)
			assert.Equal(t, content, output)
		})
	}
}

func TestRewriteActionRefs_RejectsSharedContainers(t *testing.T) {
	for name, content := range map[string]string{
		"job anchor":   "jobs:\n  a: &job\n    steps:\n      - uses: actions/checkout@v4\n  b: *job\n",
		"steps anchor": "jobs:\n  a:\n    steps: &steps\n      - uses: actions/checkout@v4\n  b:\n    steps: *steps\n",
		"step anchor":  "jobs:\n  a:\n    steps:\n      - &step\n        uses: actions/checkout@v4\n      - *step\n",
	} {
		t.Run(name, func(t *testing.T) {
			f, err := Parse("ci.yml", []byte(content))
			require.NoError(t, err)
			output, changed, err := f.RewriteActionRefs(map[string]string{"actions/checkout@v4": "actions/checkout@v4.2.2"})
			require.ErrorContains(t, err, "anchored or aliased")
			assert.Zero(t, changed)
			assert.Equal(t, content, string(output))
		})
	}
}

func TestValidateRequiredActionRefRewrites_CountsDependencySites(t *testing.T) {
	content := []byte("env: {uses: actions/cache@v4}\njobs:\n  a:\n    steps:\n      - uses: actions/cache/restore@v4\n      - uses: actions/cache/save@v4\n        with: {uses: actions/cache@v4}\n")
	f, err := Parse("ci.yml", content)
	require.NoError(t, err)
	matches, err := f.ValidateRequiredActionRefRewrites(map[string]string{
		"actions/cache@v4": "new-owner/cache@v4",
		"unused/action@v1": "unused/action@v2",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, matches["actions/cache@v4"])
	assert.NotContains(t, matches, "unused/action@v1")
	output, changed, err := f.RewriteActionRefs(map[string]string{"actions/cache@v4": "new-owner/cache@v4"})
	require.NoError(t, err)
	assert.Equal(t, 2, changed)
	assert.Contains(t, string(output), "uses: new-owner/cache/restore@v4")
	assert.Contains(t, string(output), "uses: new-owner/cache/save@v4")
}

func TestRewriteActionRefs_RejectsInvalidResult(t *testing.T) {
	content := []byte("jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n")
	f, err := Parse("ci.yml", content)
	require.NoError(t, err)
	for _, tc := range []struct {
		name        string
		replacement string
		wantErr     string
	}{
		{name: "invalid YAML", replacement: "[broken", wantErr: "validating rewritten YAML"},
		{name: "empty replacement", wantErr: "empty replacement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, changed, err := f.RewriteActionRefs(map[string]string{"actions/checkout@v4": tc.replacement})
			require.ErrorContains(t, err, tc.wantErr)
			assert.Zero(t, changed)
			assert.Equal(t, content, output)
		})
	}
}
