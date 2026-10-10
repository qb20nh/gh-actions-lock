package lockfile

import (
	"fmt"
	"sort"

	parserlock "github.com/github/actions-lockfile/go/pkg/lockfile"
)

// Validate checks the candidate that Save will write, including its graph and
// schema. It removes orphan entries from the in-memory candidate.
func (s *State) Validate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.prepareSave()
	return err
}

// prepareSave requires s.mu to be held.
func (s *State) prepareSave() ([]byte, error) {
	s.pruneOrphans()
	out, err := marshalDeterministic(s.file, s.hostname)
	if err != nil {
		return nil, err
	}
	file, err := parserlock.Parse(out)
	if err != nil {
		return nil, fmt.Errorf("validating lockfile candidate: %w", err)
	}
	if err := validateReferences(file); err != nil {
		return nil, err
	}
	if len(s.file.Dependencies) == 0 && len(s.file.Workflows) == 0 {
		return nil, nil
	}
	return append([]byte(lockfileHeader), out...), nil
}

func validateReferences(file parserlock.File) error {
	workflows := make([]string, 0, len(file.Workflows))
	for key := range file.Workflows {
		workflows = append(workflows, key)
	}
	sort.Strings(workflows)
	for _, workflow := range workflows {
		for _, key := range file.Workflows[workflow] {
			if _, ok := file.Dependencies[key]; !ok {
				return fmt.Errorf("workflow %q references missing dependency %q", workflow, key)
			}
		}
	}
	keys := make([]string, 0, len(file.Dependencies))
	for key := range file.Dependencies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, child := range file.Dependencies[key].Uses {
			if _, ok := file.Dependencies[child]; !ok {
				return fmt.Errorf("dependency %q uses missing dependency %q", key, child)
			}
		}
	}
	return nil
}
