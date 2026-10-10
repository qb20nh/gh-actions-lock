package workflowfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// RewriteActionRefs rewrites dependency uses: values without changing other YAML
// data. Unsupported scalar or alias rewrites return the original content.
func (f *File) RewriteActionRefs(replacements map[string]string) ([]byte, int, error) {
	content, changed, _, err := f.rewriteActionRefs(replacements)
	return content, changed, err
}

// ValidateRequiredActionRefRewrites reports matched dependency locations after
// checking that every matching replacement can be applied safely.
func (f *File) ValidateRequiredActionRefRewrites(replacements map[string]string) (map[string]int, error) {
	_, _, matches, err := f.rewriteActionRefs(replacements)
	return matches, err
}

type sourceEdit struct {
	start, end int
	value      string
}

func (f *File) rewriteActionRefs(replacements map[string]string) ([]byte, int, map[string]int, error) {
	original := append([]byte(nil), f.Content...)
	matches := make(map[string]int)
	if len(replacements) == 0 {
		return original, 0, matches, nil
	}

	lines := strings.Split(string(f.Content), "\n")
	lineOffsets := make([]int, len(lines))
	for i := 1; i < len(lines); i++ {
		lineOffsets[i] = lineOffsets[i-1] + len(lines[i-1]) + 1
	}
	var edits []sourceEdit
	var expected []string
	var rewriteErr error
	walkUseSites(&f.root, func(site useSite) {
		oldValue := strings.TrimSpace(site.node.Value)
		newValue, key, ok := actionRefReplacement(oldValue, replacements)
		if !ok {
			expected = append(expected, oldValue)
			return
		}
		matches[key]++
		if newValue == "" {
			if rewriteErr == nil {
				rewriteErr = fmt.Errorf("action rewrite for %s has an empty replacement", oldValue)
			}
			return
		}
		expected = append(expected, newValue)
		if newValue == oldValue {
			return
		}
		if rewriteErr != nil {
			return
		}
		if site.anchored {
			rewriteErr = fmt.Errorf("action rewrite for %s cannot update an anchored or aliased `uses:` value", oldValue)
			return
		}
		edit, err := scalarSourceEdit(site.node, newValue, lines, lineOffsets)
		if err != nil {
			rewriteErr = fmt.Errorf("action rewrite for %s: %w", oldValue, err)
			return
		}
		edits = append(edits, edit)
	})
	if rewriteErr != nil {
		return original, 0, nil, rewriteErr
	}
	if len(edits) == 0 {
		return original, 0, matches, nil
	}

	// Source coordinates describe the original bytes, so apply edits backwards.
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	content := original
	previous := len(content)
	for _, edit := range edits {
		if edit.end > previous {
			return original, 0, nil, fmt.Errorf("action rewrites have overlapping source locations")
		}
		content = append(append(append([]byte(nil), content[:edit.start]...), edit.value...), content[edit.end:]...)
		previous = edit.start
	}

	parsed, err := Parse(f.Path, content)
	if err != nil {
		return original, 0, nil, fmt.Errorf("validating rewritten YAML: %w", err)
	}
	var actual []string
	walkUseSites(&parsed.root, func(site useSite) {
		actual = append(actual, strings.TrimSpace(site.node.Value))
	})
	if len(actual) != len(expected) {
		return original, 0, nil, fmt.Errorf("action rewrite changed the number of dependency locations")
	}
	for i, value := range actual {
		if value != expected[i] {
			return original, 0, nil, fmt.Errorf("action rewrite did not preserve dependency location %d", i+1)
		}
	}
	return content, len(edits), matches, nil
}

func actionRefReplacement(value string, replacements map[string]string) (string, string, bool) {
	if replacement, ok := replacements[value]; ok {
		return replacement, value, true
	}
	replacement, ok := subpathRewriteLookup(value, replacements)
	if !ok {
		return "", "", false
	}
	at := strings.LastIndex(value, "@")
	parts := strings.SplitN(value[:at], "/", 3)
	return replacement, parts[0] + "/" + parts[1] + value[at:], true
}

func scalarSourceEdit(node *yaml.Node, value string, lines []string, offsets []int) (sourceEdit, error) {
	if node.Style&(yaml.FoldedStyle|yaml.LiteralStyle) != 0 {
		return sourceEdit{}, fmt.Errorf("multiline `uses:` values cannot be rewritten")
	}
	lineIndex := node.Line - 1
	if lineIndex < 0 || lineIndex >= len(lines) || node.Column < 1 {
		return sourceEdit{}, fmt.Errorf("invalid YAML source location")
	}
	line := lines[lineIndex]
	start, ok := columnByteOffset(line, node.Column)
	if !ok {
		return sourceEdit{}, fmt.Errorf("invalid YAML source column")
	}
	end := start
	replacement := value
	switch {
	case node.Style&yaml.SingleQuotedStyle != 0:
		var err error
		end, err = quotedScalarEnd(line, start, '\'')
		if err != nil {
			return sourceEdit{}, err
		}
		replacement = "'" + strings.ReplaceAll(value, "'", "''") + "'"
	case node.Style&yaml.DoubleQuotedStyle != 0:
		var err error
		end, err = quotedScalarEnd(line, start, '"')
		if err != nil {
			return sourceEdit{}, err
		}
		replacement = strconv.Quote(value)
	default:
		end += len(node.Value)
		if end > len(line) || line[start:end] != node.Value {
			return sourceEdit{}, fmt.Errorf("multiline or unsupported `uses:` scalar")
		}
	}

	// Only a comment immediately following this scalar can be removed. A # in
	// another flow-mapping value belongs to that value, not to this uses: field.
	tail := line[end:]
	if strings.HasPrefix(strings.TrimLeft(tail, " \t"), "#") {
		end = len(line)
		if strings.HasSuffix(line, "\r") {
			end--
		}
	}
	return sourceEdit{start: offsets[lineIndex] + start, end: offsets[lineIndex] + end, value: replacement}, nil
}

func columnByteOffset(line string, column int) (int, bool) {
	current := 1
	for offset := range line {
		if current == column {
			return offset, true
		}
		current++
	}
	return len(line), current == column
}

func quotedScalarEnd(line string, start int, quote byte) (int, error) {
	if start >= len(line) || line[start] != quote {
		return 0, fmt.Errorf("quoted `uses:` scalar does not match its source location")
	}
	for i := start + 1; i < len(line); i++ {
		if quote == '"' && line[i] == '\\' {
			i++
			continue
		}
		if line[i] != quote {
			continue
		}
		if quote == '\'' && i+1 < len(line) && line[i+1] == quote {
			i++
			continue
		}
		return i + 1, nil
	}
	return 0, fmt.Errorf("multiline quoted `uses:` values cannot be rewritten")
}

// MigrateLocalActionsToSelfRepository rewrites same-repo `./…` composite action
// references to `$/…`. Callers must opt in after accounting for checkout
// semantics; an in-repo action file does not prove runtime equivalence.
// Local reusable workflows are never candidates. It returns the new content
// and the number of dependency locations changed.
func (f *File) MigrateLocalActionsToSelfRepository() ([]byte, int, error) {
	scan := f.ExtractActionRefs()
	if err := f.validateSelfRepositoryMigration(scan); err != nil {
		return append([]byte(nil), f.Content...), 0, err
	}
	if len(scan.LocalPaths) == 0 {
		return append([]byte(nil), f.Content...), 0, nil
	}

	repoRoot := findRepoRoot(f.Path)
	replacements := make(map[string]string, len(scan.LocalPaths))
	for _, localPath := range scan.LocalPaths {
		if !localActionExists(repoRoot, localPath) {
			continue
		}
		replacements[localPath] = selfRepositoryPrefix + strings.TrimPrefix(localPath, "./")
	}
	if len(replacements) == 0 {
		return append([]byte(nil), f.Content...), 0, nil
	}

	return f.RewriteActionRefs(replacements)
}

func (f *File) validateSelfRepositoryMigration(scan RefScan) error {
	if len(scan.SelfRepositoryRefErrs) > 0 {
		return fmt.Errorf("invalid self repository reference %q", scan.SelfRepositoryRefErrs[0])
	}
	selfScan := ScanSelfRepositoryActions(f.Path, scan.SelfRepositoryActionRefs)
	if len(selfScan.SelfRepositoryRefErrs) > 0 {
		return fmt.Errorf("invalid self repository reference %q", selfScan.SelfRepositoryRefErrs[0])
	}
	if len(selfScan.Errors) > 0 {
		return fmt.Errorf("invalid self repository action: %s", selfScan.Errors[0])
	}
	return nil
}

// localActionExists reports whether a `./…` path resolves to an action file
// (action.yml or action.yaml) within the repo root.
func localActionExists(repoRoot, localPath string) bool {
	relPath := strings.TrimPrefix(localPath, "./")
	actionDir := filepath.Join(repoRoot, relPath)
	if !isWithinRoot(repoRoot, actionDir) {
		return false
	}
	for _, name := range []string{"action.yml", "action.yaml"} {
		actionPath := filepath.Join(actionDir, name)
		if err := ValidatePathWithinRoot(repoRoot, actionPath); err != nil {
			continue
		}
		info, err := os.Stat(actionPath)
		if err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// subpathRewriteLookup handles sub-path actions like actions/cache/restore@ref.
func subpathRewriteLookup(usesValue string, replacements map[string]string) (string, bool) {
	atIdx := strings.LastIndex(usesValue, "@")
	if atIdx < 0 {
		return "", false
	}
	fullName := usesValue[:atIdx]
	ref := usesValue[atIdx+1:]

	parts := strings.SplitN(fullName, "/", 3)
	if len(parts) < 3 {
		return "", false // no sub-path
	}
	nwo := parts[0] + "/" + parts[1]
	subPath := parts[2]

	nwoKey := nwo + "@" + ref
	newNWOValue, ok := replacements[nwoKey]
	if !ok {
		return "", false
	}

	newAtIdx := strings.LastIndex(newNWOValue, "@")
	if newAtIdx < 0 {
		return "", false
	}
	newRef := newNWOValue[newAtIdx+1:]
	return newNWOValue[:newAtIdx] + "/" + subPath + "@" + newRef, true
}
