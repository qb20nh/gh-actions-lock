package workflowfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractActionRefsIncludesReusableWorkflows(t *testing.T) {
	f, err := Parse("ci.yml", []byte(`jobs:
  remote:
    uses: vendor/flows/.github/workflows/build.yaml@v2
  same:
    uses: vendor/flows/.github/workflows/build.yaml@v2
  local:
    uses: ./.github/workflows/local.yml
  self:
    uses: $/.github/workflows/self.yml
  action:
    steps:
      - uses: vendor/action@v1
`))
	require.NoError(t, err)
	scan := f.ExtractActionRefs()
	assert.Equal(t, []string{"vendor/flows/.github/workflows/build.yaml@v2", "vendor/action@v1"}, actionRefStrings(scan.Refs))
	assert.Equal(t, []string{"./.github/workflows/local.yml", "$/.github/workflows/self.yml"}, scan.LocalWorkflowRefs)
	assert.Empty(t, scan.LocalPaths)
}

func TestParseRejectsIncompleteDependencyStructure(t *testing.T) {
	for name, content := range map[string]string{
		"multiple documents": "jobs: {one: {uses: vendor/flows/.github/workflows/a.yml@v1}}\n---\njobs: {two: {uses: vendor/flows/.github/workflows/b.yml@v1}}\n",
		"duplicate jobs":     "jobs: {a: {steps: [{uses: vendor/one@v1}]}}\njobs: {b: {steps: [{uses: vendor/two@v1}]}}\n",
		"merge jobs":         "template: &job {steps: [{uses: vendor/action@v1}]}\njobs: {a: {<<: *job}}\n",
		"scalar jobs":        "jobs: invalid\n",
		"scalar job":         "jobs: {a: invalid}\n",
		"mapping steps":      "jobs: {a: {steps: {uses: vendor/action@v1}}}\n",
		"mapping uses":       "jobs: {a: {steps: [{uses: {hidden: vendor/action@v1}}]}}\n",
		"empty uses":         "jobs: {a: {uses: ''}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse("ci.yml", []byte(content))
			require.Error(t, err)
		})
	}
}

func TestScanLocalWorkflowsRejectsIncompleteClosure(t *testing.T) {
	for _, tt := range []struct {
		name  string
		ref   string
		files map[string]string
		want  string
	}{
		{name: "missing workflow", ref: "./.github/workflows/missing.yml", want: "no such file"},
		{name: "invalid path", ref: "./.github/workflows/../outside.yml", want: "invalid local reusable workflow"},
		{name: "forbidden ref", ref: "./.github/workflows/local.yml@main", want: "invalid local reusable workflow"},
		{name: "missing jobs", ref: "./.github/workflows/local.yml", files: map[string]string{"local.yml": "name: incomplete\n"}, want: "nonempty jobs mapping"},
		{name: "invalid YAML", ref: "./.github/workflows/local.yml", files: map[string]string{"local.yml": "jobs: [\n"}, want: "parsing workflow YAML"},
		{name: "cycle", ref: "./.github/workflows/local.yml", files: map[string]string{
			"local.yml": "jobs: {a: {uses: ./.github/workflows/next.yaml}}\n",
			"next.yaml": "jobs: {a: {uses: $/.github/workflows/local.yml}}\n",
		}, want: "local reusable workflow cycle"},
		{name: "cycle after shared visit", ref: "./.github/workflows/local.yml", files: map[string]string{
			"local.yml": "jobs: {a: {uses: ./.github/workflows/a.yml}, b: {uses: ./.github/workflows/b.yml}}\n",
			"a.yml":     "jobs: {a: {uses: ./.github/workflows/b.yml}}\n",
			"b.yml":     "jobs: {a: {uses: ./.github/workflows/a.yml}}\n",
		}, want: "local reusable workflow cycle"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
			workflowDir := filepath.Join(root, ".github", "workflows")
			require.NoError(t, os.MkdirAll(workflowDir, 0o755))
			caller := filepath.Join(workflowDir, "ci.yml")
			require.NoError(t, os.WriteFile(caller, []byte("jobs: {a: {steps: [{run: true}]}}\n"), 0o644))
			for name, content := range tt.files {
				require.NoError(t, os.WriteFile(filepath.Join(workflowDir, name), []byte(content), 0o644))
			}
			scan := ScanSelfRepositoryActions(caller, nil, tt.ref)
			require.NotEmpty(t, scan.Errors)
			assert.Contains(t, scan.Errors[0], tt.want)
		})
	}
}

func TestScanLocalWorkflowsResolvesOnlyRepositorySymlinks(t *testing.T) {
	for _, outside := range []bool{false, true} {
		name := "in repository"
		if outside {
			name = "outside repository"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
			workflowDir := filepath.Join(root, ".github", "workflows")
			require.NoError(t, os.MkdirAll(workflowDir, 0o755))
			caller := filepath.Join(workflowDir, "ci.yml")
			require.NoError(t, os.WriteFile(caller, []byte("jobs: {a: {steps: [{run: true}]}}\n"), 0o644))
			targetRoot := root
			if outside {
				targetRoot = t.TempDir()
			}
			target := filepath.Join(targetRoot, "target.yml")
			require.NoError(t, os.WriteFile(target, []byte("jobs: {a: {steps: [{uses: vendor/action@v1}]}}\n"), 0o644))
			if err := os.Symlink(target, filepath.Join(workflowDir, "linked.yml")); err != nil {
				t.Skipf("creating symlink: %v", err)
			}
			scan := ScanSelfRepositoryActions(caller, nil, "./.github/workflows/linked.yml")
			if outside {
				require.Len(t, scan.Errors, 1)
				assert.Contains(t, scan.Errors[0], "outside repository root")
				assert.Empty(t, scan.Refs)
				return
			}
			assert.Empty(t, scan.Errors)
			assert.Equal(t, []string{"vendor/action@v1"}, actionRefStrings(scan.Refs))
			assert.Equal(t, []string{target}, scan.ActionFiles, "rewrites must target the repository file, preserving the symlink")
			want := []byte("jobs: {a: {steps: [{uses: vendor/action@v1}]}}\n")
			assert.Equal(t, want, scan.SourceContents[target])
			assert.Equal(t, want, scan.SourceContents[filepath.Join(workflowDir, "linked.yml")])
		})
	}
}
