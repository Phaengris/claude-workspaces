package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// usesTemplates reports whether the raw config exercises the template system:
// a non-empty top-level `templates:` block, or any project carrying a
// `template` or `params` key. Load uses this to decide whether the expand +
// re-marshal round-trip is needed (it isn't for the no-templates majority,
// letting strict decode run on the original bytes for exact error positions).
// `params` alone still routes through expansion so its "params without
// template" error fires.
//
// A `templates:` key that is present but null or empty declares nothing, so it
// must not cost the caller exact error positions; a malformed non-map value
// still routes through expansion, which deletes the key and lets strict decode
// judge the rest of the document.
func usesTemplates(raw map[string]any) bool {
	switch templates := raw["templates"].(type) {
	case nil:
		// Absent, or explicitly null: declares no templates.
	case map[string]any:
		if len(templates) > 0 {
			return true
		}
	default:
		return true // not a mapping; let expansion strip it
	}
	projects, _ := raw["projects"].(map[string]any)
	for _, pAny := range projects {
		project, ok := pAny.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := project["template"]; ok {
			return true
		}
		if _, ok := project["params"]; ok {
			return true
		}
	}
	return false
}

// expandTemplates resolves `templates:` on the raw YAML tree, in place,
// before strict typed decoding (spec §4). Load-time `${PARAM}` substitution
// covers only names declared in the template's params list; runtime tokens
// (${WORKSPACE}, ${PORT0}, …) pass through untouched.
//
// Every project is reported, not just the first one that fails: errors are
// collected and returned as errors.Join, matching validate's "show the whole
// list" contract. Within one project the first problem short-circuits — the
// later checks there would only echo it.
func expandTemplates(raw map[string]any) error {
	templates, _ := raw["templates"].(map[string]any)
	defer delete(raw, "templates")

	projects, _ := raw["projects"].(map[string]any)
	var errs []error
	for _, name := range sortedKeys(projects) { // stable order: the message is user-visible
		project, ok := projects[name].(map[string]any)
		if !ok {
			continue // scalar project value; strict decode will reject it with a position
		}
		tmplName, usesTemplate := project["template"].(string)
		if !usesTemplate {
			if _, hasParams := project["params"]; hasParams {
				errs = append(errs, fmt.Errorf("project %q: params without template", name))
			}
			continue
		}
		tmpl, ok := templates[tmplName].(map[string]any)
		if !ok {
			errs = append(errs, fmt.Errorf("project %q: unknown template %q", name, tmplName))
			continue
		}
		merged, err := instantiate(name, tmplName, tmpl, project)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		projects[name] = merged
	}
	return errors.Join(errs...)
}

// instantiate shallow-merges project keys over template keys and substitutes
// declared params in every string of the result.
func instantiate(project, tmplName string, tmpl, overrides map[string]any) (map[string]any, error) {
	declared := stringSlice(tmpl["params"])
	given, _ := overrides["params"].(map[string]any)

	params := make(map[string]string, len(declared))
	for _, p := range declared {
		v, ok := given[p]
		if !ok {
			return nil, fmt.Errorf("project %q: missing param %q of template %q", project, p, tmplName)
		}
		params[p] = fmt.Sprint(v)
	}
	var undeclared []string
	for p := range given {
		if _, ok := params[p]; !ok {
			undeclared = append(undeclared, p)
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		return nil, fmt.Errorf("project %q: undeclared param %q for template %q", project, undeclared[0], tmplName)
	}

	merged := make(map[string]any, len(tmpl)+len(overrides))
	for k, v := range tmpl {
		if k != "params" {
			merged[k] = v
		}
	}
	for k, v := range overrides {
		if k != "template" && k != "params" {
			merged[k] = v // shallow: a project key replaces the template key wholesale
		}
	}
	return substitute(merged, params).(map[string]any), nil
}

// substitute walks the tree and replaces ${PARAM} in every string.
func substitute(v any, params map[string]string) any {
	switch node := v.(type) {
	case string:
		for p, val := range params {
			node = strings.ReplaceAll(node, "${"+p+"}", val)
		}
		return node
	case map[string]any:
		out := make(map[string]any, len(node))
		for k, child := range node {
			out[k] = substitute(child, params)
		}
		return out
	case []any:
		out := make([]any, len(node))
		for i, child := range node {
			out[i] = substitute(child, params)
		}
		return out
	default:
		return v
	}
}

// stringSlice converts a YAML sequence of scalars to []string (nil-safe).
func stringSlice(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, fmt.Sprint(item))
	}
	return out
}
