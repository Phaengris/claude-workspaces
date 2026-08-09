// Package wsp derives everything about a workspace from reality: the
// allocation registry, the filesystem, and git. The read side stores nothing;
// the writers (write.go) only ever emit files that are themselves projections
// of that derived state, so regenerating one can never lose information.
package wsp

import (
	"strings"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/config"
)

// Subst replaces ${K} for every key in vars. Unknown tokens pass through
// untouched — runtime tokens and load-time template params share one syntax,
// and passing unknowns through is what makes that safe (spec §4). Replacement
// is single-pass per key in map order; if a substituted value itself contains
// another key's ${K}, the result is order-dependent and unsupported — values
// must not be token-bearing after substitution.
func Subst(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
	}
	return s
}

// RuntimeVars builds the substitution set for one workspace(+project):
// WORKSPACE, PROJECT (only when project != ""), and the index-derived values
// from alloc.ComputeValues. The returned map is fresh and safe to mutate.
func RuntimeVars(cfg *config.Config, taskID, project string, index int) map[string]string {
	vars := alloc.ComputeValues(cfg.Values, index)
	vars["WORKSPACE"] = taskID
	if project != "" {
		vars["PROJECT"] = project
	}
	return vars
}

// ResolvedEnv merges global cfg.Env with the named project's env (project wins
// per key) and substitutes runtime tokens in every VALUE. Keys are never
// substituted. An empty or unknown project yields the global env alone.
func ResolvedEnv(cfg *config.Config, taskID, project string, index int) map[string]string {
	vars := RuntimeVars(cfg, taskID, project, index)
	out := make(map[string]string, len(cfg.Env))
	for k, v := range cfg.Env {
		out[k] = Subst(v, vars)
	}
	if p := cfg.Projects[project]; p != nil {
		for k, v := range p.Env {
			out[k] = Subst(v, vars)
		}
	}
	return out
}
