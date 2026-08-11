package wsp

import (
	"fmt"
	"slices"

	"github.com/Phaengris/claude-workspaces/internal/config"
)

// TopoOrder orders the given project names so that every project comes after
// the projects it depends on — the order `up` starts them in, and the reverse
// of the order `down` stops them in.
//
// Only the given names participate: a workspace holds a subset of the
// configured projects, and a dependency on something not checked out here is
// ignored rather than pulled in (spec §7). A name with no config entry simply
// has no dependencies.
//
// The order is deterministic, not merely valid: among the projects whose
// dependencies are all satisfied, the alphabetically first goes next. Two runs
// over the same workspace therefore set up projects in the same order, which
// keeps output and logs comparable.
//
// A cycle is an error. Config validation already rejects cyclic depends, so
// this is defence in depth rather than a reachable user error.
func TopoOrder(cfg *config.Config, names []string) ([]string, error) {
	inSet := make(map[string]bool, len(names))
	for _, n := range names {
		inSet[n] = true
	}

	// remaining[name] counts unsatisfied dependencies; dependents[name] lists
	// the projects waiting on it.
	remaining := make(map[string]int, len(inSet))
	dependents := make(map[string][]string)
	for name := range inSet {
		remaining[name] = 0
		p := cfg.Projects[name]
		if p == nil {
			continue
		}
		for _, dep := range p.Depends {
			if !inSet[dep] {
				continue
			}
			remaining[name]++
			dependents[dep] = append(dependents[dep], name)
		}
	}

	var ready []string
	for name, n := range remaining {
		if n == 0 {
			ready = append(ready, name)
		}
	}
	slices.Sort(ready)

	var out []string
	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		out = append(out, name)
		var freed []string
		for _, next := range dependents[name] {
			if remaining[next]--; remaining[next] == 0 {
				freed = append(freed, next)
			}
		}
		if len(freed) > 0 {
			ready = append(ready, freed...)
			slices.Sort(ready)
		}
	}

	if len(out) < len(remaining) {
		var cyclic []string
		for name, n := range remaining {
			if n > 0 {
				cyclic = append(cyclic, name)
			}
		}
		slices.Sort(cyclic)
		return nil, fmt.Errorf("dependency cycle among checked-out projects: %v", cyclic)
	}
	return out, nil
}
