// Package workflowfile owns the parsed workflow YAML representation: loading,
// extraction of action refs, local composite discovery, and comment-preserving
// rewriting. It intentionally has no dependency on the lockfile or resolver
// packages.
package workflowfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
	"gopkg.in/yaml.v3"
)

var errPathOutsideRoot = errors.New("path resolves outside repository root")

// File is the parsed workflow YAML the CLI rewrites in-place.
// It carries the original byte content alongside the parsed node tree so
// RewriteActionRefs can do anchored, comment-preserving substitution.
type File struct {
	Path    string
	Content []byte
	root    yaml.Node
}

// Load reads and parses a workflow file.
func Load(path string) (*File, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading workflow: %w", err)
	}

	return Parse(path, content)
}

// Parse builds a File from already-loaded workflow content.
func Parse(path string, content []byte) (*File, error) {
	f := &File{
		Path:    path,
		Content: content,
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&f.root); err != nil {
		return nil, fmt.Errorf("parsing workflow YAML: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("workflow YAML must contain exactly one document")
	}
	if err := validateYAMLTree(&f.root, 0); err != nil {
		return nil, err
	}
	if err := validateUseContainers(documentRoot(&f.root)); err != nil {
		return nil, err
	}

	var invalid []string
	walkUses(&f.root, func(value string, _ bool) {
		if isUsesExpression(value) {
			invalid = append(invalid, strings.TrimSpace(value))
		}
	})
	if len(invalid) > 0 {
		return nil, usesExpressionErr(invalid)
	}

	return f, nil
}

// isUsesExpression reports whether a `uses:` value contains an expression.
// Malformed `$/…@ref` values are excluded so the more specific self
// repository diagnostic still wins.
func isUsesExpression(value string) bool {
	value = strings.TrimSpace(value)
	return strings.Contains(value, "${") && !SelfRepositoryRefHasVersion(value)
}

// usesExpressionErr reports an expression in `uses:`. GitHub Actions rejects
// the workflow outright at both step and job level (no context is in scope
// there), so parsing fails rather than skipping the line.
func usesExpressionErr(values []string) error {
	return fmt.Errorf("`uses:` can't contain an expression: %s", strings.Join(values, ", "))
}

func appendDockerWarning(warnings *[]string, seen map[string]bool, value string) bool {
	if !strings.HasPrefix(value, "docker://") {
		return false
	}
	if seen[value] {
		return true
	}
	seen[value] = true
	if strings.Contains(strings.TrimPrefix(value, "docker://"), "@") {
		return true
	}
	*warnings = append(*warnings, fmt.Sprintf("%s is not covered by the actions lockfile; container image tags are mutable", value))
	return true
}

// RefScan is the classified result of walking a workflow's `uses:` values.
type RefScan struct {
	// Refs are remote repository action references (owner/repo[/path]@ref).
	Refs []parserlock.ActionRef
	// LocalPaths are `./…` local composite action references (reusable
	// workflows are excluded — they resolve differently).
	LocalPaths []string
	// SelfRepositoryRefs are valid `$/…` self repository actions. Inherently
	// pinned: they resolve against the defining repo at the running ref.
	SelfRepositoryRefs []string
	// SelfRepositoryActionRefs are the step-level subset that must be scanned
	// locally for remote dependencies. Job-level refs point at reusable
	// workflows, which the workflow discovery pass scans independently.
	SelfRepositoryActionRefs []string
	// LocalWorkflowRefs are job-level calls into the defining repository.
	LocalWorkflowRefs []string
	// SelfRepositoryRefErrs are malformed `$/…@ref` values — the invalid form.
	SelfRepositoryRefErrs []string
	// Warnings are non-fatal parse notes (e.g. expression-based uses:).
	Warnings []string
}

// UseSite identifies a reference at a job or step position recognized by Actions.
type UseSite struct {
	Value     string
	StepLevel bool
}

// Uses returns the workflow and composite-action references in source order.
func (f *File) Uses() []UseSite {
	var uses []UseSite
	walkUses(&f.root, func(value string, stepLevel bool) {
		uses = append(uses, UseSite{Value: strings.TrimSpace(value), StepLevel: stepLevel})
	})
	return uses
}

// WorkflowUses requires a jobs mapping before treating a fetched or locally
// called workflow as a dependency manifest.
func (f *File) WorkflowUses() ([]UseSite, error) {
	jobs := mapValue(documentRoot(&f.root), "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode || len(jobs.Content) == 0 {
		return nil, fmt.Errorf("workflow must contain a nonempty jobs mapping")
	}
	return f.Uses(), nil
}

func validateYAMLTree(node *yaml.Node, depth int) error {
	if node == nil {
		return nil
	}
	if depth > maxYAMLWalkDepth {
		return fmt.Errorf("YAML nesting exceeds max depth %d", maxYAMLWalkDepth)
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool)
		for i := 0; i < len(node.Content)-1; i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode {
				return fmt.Errorf("YAML mapping keys must be scalars at line %d", key.Line)
			}
			if seen[key.Value] {
				return fmt.Errorf("duplicate YAML key %q at line %d", key.Value, key.Line)
			}
			seen[key.Value] = true
			if key.Value == "<<" {
				return fmt.Errorf("YAML merge keys are unsupported at line %d", key.Line)
			}
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLTree(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateUseContainers(root *yaml.Node) error {
	root = resolveAlias(root)
	if root == nil || root.Kind != yaml.MappingNode {
		return fmt.Errorf("workflow or action metadata must contain a YAML mapping")
	}
	if runs := mapValue(root, "runs"); runs != nil {
		if runs.Kind != yaml.MappingNode {
			return fmt.Errorf("runs must be a mapping")
		}
		if err := validateSteps(runs); err != nil {
			return err
		}
	}
	if jobs := mapValue(root, "jobs"); jobs != nil {
		if jobs.Kind != yaml.MappingNode {
			return fmt.Errorf("jobs must be a mapping")
		}
		for i := 1; i < len(jobs.Content); i += 2 {
			job := resolveAlias(jobs.Content[i])
			if job == nil || job.Kind != yaml.MappingNode {
				return fmt.Errorf("job at line %d must be a mapping", jobs.Content[i].Line)
			}
			if err := validateUseValue(job); err != nil {
				return err
			}
			if err := validateSteps(job); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateUseValue(owner *yaml.Node) error {
	if use := mapValue(owner, "uses"); use != nil && (use.Kind != yaml.ScalarNode || use.Tag != "!!str" || strings.TrimSpace(use.Value) == "") {
		return fmt.Errorf("uses must be a nonempty string at line %d", use.Line)
	}
	return nil
}

func validateSteps(owner *yaml.Node) error {
	steps := mapValue(owner, "steps")
	if steps == nil {
		return nil
	}
	if steps.Kind != yaml.SequenceNode {
		return fmt.Errorf("steps must be a sequence at line %d", steps.Line)
	}
	for _, raw := range steps.Content {
		step := resolveAlias(raw)
		if step == nil || step.Kind != yaml.MappingNode {
			return fmt.Errorf("step at line %d must be a mapping", raw.Line)
		}
		if err := validateUseValue(step); err != nil {
			return err
		}
	}
	return nil
}

// ExtractActionRefs finds and classifies all uses: references in the workflow.
func (f *File) ExtractActionRefs() RefScan {
	var scan RefScan
	seen := make(map[string]bool)
	seenLocal := make(map[string]bool)
	seenSelf := make(map[string]bool)
	seenSelfAction := make(map[string]bool)
	seenWorkflow := make(map[string]bool)
	seenDocker := make(map[string]bool)

	walkUses(&f.root, func(value string, stepLevel bool) {
		value = strings.TrimSpace(value)
		if SelfRepositoryRefHasVersion(value) {
			if !seenSelf[value] {
				seenSelf[value] = true
				scan.SelfRepositoryRefErrs = append(scan.SelfRepositoryRefErrs, value)
			}
			return
		}
		if IsSelfRepositoryAction(value) {
			// Bare `$/…` is a legal self repository reference at both step level
			// (action dir) and job level (reusable-workflow file): "this repo
			// at the running SHA", inherently pinned. Only the `@ref` form is
			// invalid — a self repository reference has no external ref to pin.
			if !seenSelf[value] {
				seenSelf[value] = true
				scan.SelfRepositoryRefs = append(scan.SelfRepositoryRefs, value)
			}
			if stepLevel && !seenSelfAction[value] {
				seenSelfAction[value] = true
				scan.SelfRepositoryActionRefs = append(scan.SelfRepositoryActionRefs, value)
			}
			if !stepLevel && !seenWorkflow[value] {
				seenWorkflow[value] = true
				scan.LocalWorkflowRefs = append(scan.LocalWorkflowRefs, value)
			}
			return
		}
		if strings.HasPrefix(value, "./") {
			if !stepLevel {
				if !seenWorkflow[value] {
					seenWorkflow[value] = true
					scan.LocalWorkflowRefs = append(scan.LocalWorkflowRefs, value)
				}
				return
			}
			if !seenLocal[value] {
				seenLocal[value] = true
				scan.LocalPaths = append(scan.LocalPaths, value)
			}
			return
		}
		if appendDockerWarning(&scan.Warnings, seenDocker, value) {
			return
		}
		actionRef := parserlock.ParseActionRef(value)
		if !stepLevel {
			if ref := parserlock.ParseReusableWorkflowRef(value); ref != nil {
				actionRef = &parserlock.ActionRef{Owner: ref.Owner, Repo: ref.Repo, Path: ref.Path, Ref: ref.Ref, Raw: ref.Raw}
			}
		}
		if actionRef != nil {
			dedupKey := actionRef.FullName() + "@" + actionRef.Ref
			if !seen[dedupKey] {
				seen[dedupKey] = true
				scan.Refs = append(scan.Refs, *actionRef)
			}
		}
	})

	return scan
}

// SelfRepositoryActionScan is the dependency closure of repository-owned
// actions and local reusable workflows.
type SelfRepositoryActionScan struct {
	Refs                  []parserlock.ActionRef
	SelfRepositoryRefs    []string
	SelfRepositoryRefErrs []string
	LocalPaths            []string
	// ActionFiles includes reachable action and workflow definitions in visit
	// order. Their remote references are rewritable source.
	ActionFiles []string
	// SourceContents retains the bytes used to discover dependencies so a
	// later writer can reject changes after scanning.
	SourceContents map[string][]byte
	// SourcePaths records the resolved identity of every source read path.
	SourcePaths map[string]string
	Errors      []string
	Warnings    []string
}

// ScanSelfRepositoryActions reads local definitions at the repository root,
// including workflowRefs from job-level ./… and $/… calls.
func ScanSelfRepositoryActions(workflowPath string, actionRefs []string, workflowRefs ...string) SelfRepositoryActionScan {
	var scan SelfRepositoryActionScan
	if len(actionRefs) == 0 && len(workflowRefs) == 0 {
		return scan
	}

	repoRoot := findRepoRoot(workflowPath)
	if repoRoot == "" {
		scan.Errors = append(scan.Errors, "can't inspect repository definitions: not in a git repository")
		return scan
	}
	scan.SourceContents = make(map[string][]byte)
	scan.SourcePaths = make(map[string]string)

	type pendingDefinition struct {
		ref       string
		workflow  bool
		caller    string
		ancestors []string
	}
	pending := make([]pendingDefinition, 0, len(actionRefs)+len(workflowRefs))
	var ancestors []string
	if abs, err := filepath.Abs(workflowPath); err == nil {
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			ancestors = []string{resolved}
		}
	}
	for _, ref := range actionRefs {
		pending = append(pending, pendingDefinition{ref: strings.TrimSpace(ref), ancestors: ancestors})
	}
	for _, ref := range workflowRefs {
		caller := ""
		if len(ancestors) != 0 {
			caller = ancestors[0]
		}
		pending = append(pending, pendingDefinition{ref: strings.TrimSpace(ref), workflow: true, caller: caller, ancestors: ancestors})
	}

	seenFiles := make(map[string]bool)
	seenRefs := make(map[string]bool)
	seenSelf := make(map[string]bool)
	seenInvalid := make(map[string]bool)
	seenLocal := make(map[string]bool)
	seenDocker := make(map[string]bool)
	workflowEdges := make(map[string][]string)

	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		if len(current.ancestors) > maxYAMLWalkDepth {
			scan.Errors = append(scan.Errors, fmt.Sprintf("repository definition recursion exceeded max depth %d at %s", maxYAMLWalkDepth, current.ref))
			continue
		}
		if SelfRepositoryRefHasVersion(current.ref) {
			if !seenInvalid[current.ref] {
				seenInvalid[current.ref] = true
				scan.SelfRepositoryRefErrs = append(scan.SelfRepositoryRefErrs, current.ref)
			}
			continue
		}

		relPath := strings.TrimPrefix(strings.TrimPrefix(current.ref, selfRepositoryPrefix), "./")
		definitionPath := filepath.Join(repoRoot, filepath.FromSlash(relPath))
		if !isWithinRoot(repoRoot, definitionPath) {
			scan.Errors = append(scan.Errors, fmt.Sprintf("refusing to read repository definition outside repo root: %s", current.ref))
			continue
		}

		var uses []UseSite
		var filePath string
		var content []byte
		var err error
		if current.workflow {
			if strings.Contains(relPath, "@") || parserlock.ParseReusableWorkflowRef("self/repo/"+relPath+"@HEAD") == nil {
				scan.Errors = append(scan.Errors, fmt.Sprintf("invalid local reusable workflow %q", current.ref))
				continue
			}
			filePath = definitionPath
			err = ValidatePathWithinRoot(repoRoot, filePath)
			if err == nil {
				var f *File
				f, err = Load(filePath)
				if err == nil {
					content = f.Content
					uses, err = f.WorkflowUses()
				}
			}
		} else {
			var values []string
			values, filePath, content, err = readActionUses(repoRoot, definitionPath)
			for _, value := range values {
				uses = append(uses, UseSite{Value: value, StepLevel: true})
			}
		}
		if err != nil {
			scan.Errors = append(scan.Errors, fmt.Sprintf("can't inspect repository definition %s: %v", current.ref, err))
			continue
		}
		if _, seen := scan.SourceContents[filePath]; !seen {
			scan.SourceContents[filePath] = content
		}
		resolvedPath, err := filepath.EvalSymlinks(filePath)
		if err != nil {
			scan.Errors = append(scan.Errors, fmt.Sprintf("resolving repository definition %s: %v", current.ref, err))
			continue
		}
		if _, seen := scan.SourcePaths[filePath]; !seen {
			scan.SourcePaths[filePath] = resolvedPath
		}
		filePath = resolvedPath
		if current.workflow && current.caller != "" {
			workflowEdges[current.caller] = append(workflowEdges[current.caller], filePath)
		}
		if seenFiles[filePath] {
			continue
		}
		seenFiles[filePath] = true
		if _, seen := scan.SourceContents[filePath]; !seen {
			scan.SourceContents[filePath] = content
		}
		scan.SourcePaths[filePath] = filePath
		scan.ActionFiles = append(scan.ActionFiles, filePath)
		ancestors := append(append([]string(nil), current.ancestors...), filePath)
		for _, site := range uses {
			use := strings.TrimSpace(site.Value)
			switch {
			case SelfRepositoryRefHasVersion(use):
				if !seenInvalid[use] {
					seenInvalid[use] = true
					scan.SelfRepositoryRefErrs = append(scan.SelfRepositoryRefErrs, use)
				}
			case IsSelfRepositoryAction(use):
				if !seenSelf[use] {
					seenSelf[use] = true
					scan.SelfRepositoryRefs = append(scan.SelfRepositoryRefs, use)
				}
				pending = append(pending, pendingDefinition{ref: use, workflow: !site.StepLevel, caller: filePath, ancestors: ancestors})
			case strings.HasPrefix(use, "./"):
				if !site.StepLevel {
					pending = append(pending, pendingDefinition{ref: use, workflow: true, caller: filePath, ancestors: ancestors})
					continue
				}
				if !seenLocal[use] {
					seenLocal[use] = true
					scan.LocalPaths = append(scan.LocalPaths, use)
				}
			case appendDockerWarning(&scan.Warnings, seenDocker, use):
			default:
				var actionRef *parserlock.ActionRef
				if site.StepLevel {
					actionRef = parserlock.ParseActionRef(use)
				} else {
					if ref := parserlock.ParseReusableWorkflowRef(use); ref != nil {
						actionRef = &parserlock.ActionRef{Owner: ref.Owner, Repo: ref.Repo, Path: ref.Path, Ref: ref.Ref, Raw: ref.Raw}
					}
				}
				if actionRef == nil {
					scan.Errors = append(scan.Errors, fmt.Sprintf("invalid dependency reference %q in %s", use, filePath))
					continue
				}
				key := actionRef.FullName() + "@" + actionRef.Ref
				if !seenRefs[key] {
					seenRefs[key] = true
					scan.Refs = append(scan.Refs, *actionRef)
				}
			}
		}
	}

	if cycle := workflowCycle(workflowEdges); cycle != "" {
		scan.Errors = append(scan.Errors, fmt.Sprintf("local reusable workflow cycle at %s", cycle))
	}
	return scan
}

func workflowCycle(edges map[string][]string) string {
	visited, active := make(map[string]bool), make(map[string]bool)
	var visit func(string) string
	visit = func(file string) string {
		if active[file] {
			return file
		}
		if visited[file] {
			return ""
		}
		visited[file], active[file] = true, true
		for _, child := range edges[file] {
			if cycle := visit(child); cycle != "" {
				return cycle
			}
		}
		active[file] = false
		return ""
	}
	files := make([]string, 0, len(edges))
	for file := range edges {
		files = append(files, file)
	}
	sort.Strings(files)
	for _, file := range files {
		if cycle := visit(file); cycle != "" {
			return cycle
		}
	}
	return ""
}

// DiscoverWorkflows finds all workflow files in .github/workflows/ relative to
// the current directory. Returns nil if the directory doesn't exist.
func DiscoverWorkflows() ([]string, error) {
	return DiscoverWorkflowsIn(filepath.Join(".github", "workflows"))
}

// DiscoverWorkflowsIn finds all workflow files (*.yml, *.yaml) in dir.
// Returns nil if the directory doesn't exist.
func DiscoverWorkflowsIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := filepath.Ext(entry.Name())
		if ext == ".yml" || ext == ".yaml" {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// FindRepoRoot returns the git repository root containing startPath, or "" when
// startPath is not inside a git repository.
func FindRepoRoot(startPath string) string {
	return findRepoRoot(startPath)
}

// DiscoverCompositeActionFiles walks the repository rooted at root and returns
// the paths of all action definition files (action.yml / action.yaml). The
// .git directory is skipped. Non-composite action files are included; callers
// migrate them with MigrateLocalActionsToSelfRepository, which no-ops when a file has
// no local `./…` steps.
func DiscoverCompositeActionFiles(root string) ([]string, error) {
	if root == "" {
		return nil, nil
	}
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "action.yml" || d.Name() == "action.yaml" {
			if err := ValidatePathWithinRoot(root, p); err != nil {
				return err
			}
			info, err := os.Stat(p)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("action definition %s is not a regular file", p)
			}
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discovering action files under %s: %w", root, err)
	}
	sort.Strings(paths)
	return paths, nil
}

// ExtractLocalCompositeRefs reads action.yml files from local paths relative
// to the workflow file's directory and returns any repository action refs
// found in their steps.
func ExtractLocalCompositeRefs(workflowPath string, localPaths []string) ([]parserlock.ActionRef, []string) {
	var refs []parserlock.ActionRef
	var warnings []string
	seen := make(map[string]bool)

	repoRoot := findRepoRoot(workflowPath)
	if repoRoot == "" {
		if len(localPaths) > 0 {
			warnings = append(warnings, "can't resolve local action paths: not in a git repository")
		}
		return nil, warnings
	}

	for _, localPath := range localPaths {
		relPath := strings.TrimPrefix(localPath, "./")
		actionDir := filepath.Join(repoRoot, relPath)
		if !isWithinRoot(repoRoot, actionDir) {
			warnings = append(warnings, fmt.Sprintf("refusing to read action file outside repo root: %s", localPath))
			continue
		}

		actionContent, err := os.ReadFile(filepath.Join(actionDir, "action.yml"))
		if err != nil {
			actionContent, err = os.ReadFile(filepath.Join(actionDir, "action.yaml"))
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("can't read action file for %s: %v", localPath, err))
				continue
			}
		}

		uses, parseErr := parseActionYAMLForUses(actionContent)
		if parseErr != nil {
			warnings = append(warnings, fmt.Sprintf("can't parse action file for %s: %v", localPath, parseErr))
			continue
		}

		for _, use := range uses {
			actionRef := parserlock.ParseActionRef(use)
			if actionRef != nil {
				dedupKey := actionRef.FullName() + "@" + actionRef.Ref
				if !seen[dedupKey] {
					seen[dedupKey] = true
					refs = append(refs, *actionRef)
				}
			}
		}
	}

	return refs, warnings
}

// walkUses visits `uses:` values at the only places GitHub Actions honors
// them: `jobs.<id>.uses`, `jobs.<id>.steps[*].uses` in a workflow, and
// `runs.steps[*].uses` in a composite action definition. A `uses` key
// anywhere else (an env var, a `with:` input) is ordinary data, not a
// reference.
func walkUses(node *yaml.Node, fn func(value string, stepLevel bool)) {
	walkUseSites(node, func(site useSite) {
		if use := scalarValue(site.node); use != "" {
			fn(use, site.stepLevel)
		}
	})
}

func documentRoot(node *yaml.Node) *yaml.Node {
	if node != nil && node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return node.Content[0]
	}
	return node
}

// resolveAlias dereferences YAML anchors so an aliased job or step is walked
// like the node it points at.
func resolveAlias(node *yaml.Node) *yaml.Node {
	for i := 0; node != nil && node.Kind == yaml.AliasNode && i < maxYAMLWalkDepth; i++ {
		node = node.Alias
	}
	return node
}

func mapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content)-1; i += 2 {
		if node.Content[i].Kind == yaml.ScalarNode && node.Content[i].Value == key {
			return resolveAlias(node.Content[i+1])
		}
	}
	return nil
}

func scalarValue(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}

// maxYAMLWalkDepth bounds YAML nesting, aliases, and local definition traversal.
const maxYAMLWalkDepth = 100

func findRepoRoot(startPath string) string {
	absPath, err := filepath.Abs(filepath.Dir(startPath))
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(absPath, ".git")); err == nil {
			return absPath
		}
		parent := filepath.Dir(absPath)
		if parent == absPath {
			return ""
		}
		absPath = parent
	}
}

func isWithinRoot(root, candidate string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absCandidate)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

// ValidatePathWithinRoot resolves symlinks in candidate and rejects paths that
// leave root. Callers use it before reading or rewriting repository-owned YAML.
func ValidatePathWithinRoot(root, candidate string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolving repository root %s: %w", root, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return fmt.Errorf("resolving repository root %s: %w", root, err)
	}
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return fmt.Errorf("resolving repository path %s: %w", candidate, err)
	}
	resolvedCandidate, err := filepath.EvalSymlinks(absCandidate)
	if err != nil {
		return fmt.Errorf("resolving repository path %s: %w", candidate, err)
	}
	if !isWithinRoot(resolvedRoot, resolvedCandidate) {
		return fmt.Errorf("%w: %s is outside %s", errPathOutsideRoot, candidate, root)
	}
	return nil
}

func parseActionYAMLForUses(content []byte) ([]string, error) {
	f, err := Parse("action.yml", content)
	if err != nil {
		return nil, err
	}
	meta, err := parserlock.ParseActionMeta(string(content))
	if err != nil {
		return nil, err
	}
	if meta.Execution == parserlock.ExecUnknown {
		return nil, fmt.Errorf("action manifest has no supported runs.using value")
	}
	if meta.Execution != parserlock.ExecComposite {
		return nil, nil
	}

	var uses []string
	for _, site := range f.Uses() {
		uses = append(uses, site.Value)
	}
	return uses, nil
}

// readActionUses returns the `uses:` values of an in-repo action definition
// along with the file they were read from, so callers can rewrite that file.
func readActionUses(repoRoot, actionDir string) ([]string, string, []byte, error) {
	var lastErr error
	for _, name := range []string{"action.yml", "action.yaml"} {
		actionPath := filepath.Join(actionDir, name)
		if err := ValidatePathWithinRoot(repoRoot, actionPath); err != nil {
			if errors.Is(err, errPathOutsideRoot) {
				return nil, "", nil, err
			}
			lastErr = err
			continue
		}
		content, err := os.ReadFile(actionPath)
		if err != nil {
			lastErr = err
			continue
		}
		uses, err := parseActionYAMLForUses(content)
		if err != nil {
			return nil, "", nil, fmt.Errorf("parsing %s: %w", name, err)
		}
		return uses, actionPath, content, nil
	}
	return nil, "", nil, fmt.Errorf("reading action.yml or action.yaml: %w", lastErr)
}

// KeyFromPath converts a workflow path discovered on disk (relative to the
// repo root or cwd) into the repo-relative key used inside the lockfile.
func KeyFromPath(workflowPath string) string {
	cleaned := filepath.ToSlash(filepath.Clean(workflowPath))
	cleaned = strings.TrimPrefix(cleaned, "./")
	return cleaned
}
