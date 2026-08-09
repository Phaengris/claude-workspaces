package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// validate reports every problem at once (errors.Join), not just the first —
// doctor prints the full list (spec §2). A nil return means the config is
// structurally sound: every project has a repo, every dependency names a known
// project, every value's start/per_workspace are in range, and the depends
// graph is acyclic.
func (c *Config) validate() error {
	var errs []error
	for _, name := range sortedKeys(c.Projects) {
		p := c.Projects[name]
		if p == nil {
			errs = append(errs, fmt.Errorf("project %q: empty definition", name))
			continue
		}
		if p.Repo == "" {
			errs = append(errs, fmt.Errorf("project %q: repo is required", name))
		}
		if p.Path != "" && !safeProjectPath(p.Path) {
			errs = append(errs, fmt.Errorf("project %q: path must be relative and must not escape the workspace (got %q)", name, p.Path))
		}
		for _, dep := range p.Depends {
			if _, ok := c.Projects[dep]; !ok {
				errs = append(errs, fmt.Errorf("project %q: depends on unknown project %q", name, dep))
			}
		}
	}
	for _, name := range sortedKeys(c.Values) {
		v := c.Values[name]
		if v.Start <= 0 {
			errs = append(errs, fmt.Errorf("value %q: start must be positive", name))
		}
		if v.PerWorkspace < 1 {
			errs = append(errs, fmt.Errorf("value %q: per_workspace must be at least 1", name))
		}
	}
	if err := checkCycles(c.Projects); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// safeProjectPath reports whether a project's `path` stays strictly inside
// the workspace dir it will be joined onto. `path` is where destroy (and
// new's undo) force-remove a worktree, so an escaping value would let config
// point removal at a FOREIGN directory — rejected at load time, before any
// command can act on it. Three rules, checked on the value as written:
// absolute paths are out; ANY `..` component is out, even one that cleans
// away harmlessly (the literal rule spares everyone reasoning about Clean
// semantics); and a path that cleans to "." is out — it would name the
// workspace dir itself, not a location inside it. The empty path never gets
// here: it means "default to the project's config key" (validate guards).
func safeProjectPath(path string) bool {
	if filepath.IsAbs(path) {
		return false
	}
	if filepath.Clean(path) == "." {
		return false
	}
	for _, comp := range strings.Split(path, string(filepath.Separator)) {
		if comp == ".." {
			return false
		}
	}
	return true
}

// checkCycles runs Kahn's algorithm over the depends graph; whatever cannot
// be topologically ordered is part of a cycle. Unknown dependencies are
// skipped here (validate reports them separately) so a missing edge never
// masquerades as a cycle.
func checkCycles(projects map[string]*Project) error {
	indegree := make(map[string]int, len(projects))
	dependents := make(map[string][]string)
	for name, p := range projects {
		indegree[name] += 0
		if p == nil {
			continue
		}
		for _, dep := range p.Depends {
			if _, ok := projects[dep]; !ok {
				continue // unknown deps are reported separately
			}
			indegree[name]++
			dependents[dep] = append(dependents[dep], name)
		}
	}
	queue := []string{}
	for name, deg := range indegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}
	done := 0
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		done++
		for _, next := range dependents[name] {
			if indegree[next]--; indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if done < len(indegree) {
		var cyclic []string
		for name, deg := range indegree {
			if deg > 0 {
				cyclic = append(cyclic, name)
			}
		}
		sort.Strings(cyclic)
		return fmt.Errorf("dependency cycle involving: %v", cyclic)
	}
	return nil
}

// sortedKeys gives deterministic iteration order — Go maps randomize theirs,
// and user-visible output (and error lists) must be stable (spec §4).
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
