package config

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// rawTree parses YAML into the raw map form Load feeds to expandTemplates.
func rawTree(t *testing.T, yml string) map[string]any {
	t.Helper()
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(yml), &raw); err != nil {
		t.Fatalf("test yaml: %v", err)
	}
	return raw
}

func TestExpandSubstitutesParamsAndMerges(t *testing.T) {
	raw := rawTree(t, `
templates:
  client:
    params: [NAME]
    repo: ~/dev/clients/${NAME}
    base_branch: develop
    env:
      CUSTOMER: "${NAME}-${WORKSPACE}"
projects:
  acme:
    template: client
    params: { NAME: acme }
    base_branch: main
`)
	if err := expandTemplates(raw); err != nil {
		t.Fatalf("expandTemplates: %v", err)
	}
	if _, still := raw["templates"]; still {
		t.Error("templates key must be deleted after expansion")
	}
	acme := raw["projects"].(map[string]any)["acme"].(map[string]any)
	if got := acme["repo"]; got != "~/dev/clients/acme" {
		t.Errorf("repo = %v (param not substituted?)", got)
	}
	if got := acme["base_branch"]; got != "main" {
		t.Errorf("base_branch = %v; project keys must override template keys", got)
	}
	env := acme["env"].(map[string]any)
	if got := env["CUSTOMER"]; got != "acme-${WORKSPACE}" {
		t.Errorf("CUSTOMER = %v; runtime tokens must pass through untouched", got)
	}
	for _, leftover := range []string{"template", "params"} {
		if _, still := acme[leftover]; still {
			t.Errorf("%s key must be consumed by expansion", leftover)
		}
	}
}

func TestExpandErrors(t *testing.T) {
	cases := []struct {
		name, yml, wantErr string
	}{
		{
			"unknown template",
			`{projects: {p: {template: nope, params: {}}}}`,
			"unknown template",
		},
		{
			"missing param",
			`{templates: {c: {params: [NAME], repo: /r}}, projects: {p: {template: c}}}`,
			"missing param",
		},
		{
			"undeclared param",
			`{templates: {c: {params: [], repo: /r}}, projects: {p: {template: c, params: {EXTRA: x}}}}`,
			"undeclared param",
		},
		{
			"params without template",
			`{projects: {p: {repo: /r, params: {NAME: x}}}}`,
			"params without template",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := expandTemplates(rawTree(t, tc.yml))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestExpandSubstitutesInLists covers substitute()'s []any branch — the
// highest-traffic templated shape (setup/teardown/start lists). A declared
// ${PARAM} is substituted inside every list entry; runtime tokens like
// ${PORT0} in the same list pass through untouched.
func TestExpandSubstitutesInLists(t *testing.T) {
	raw := rawTree(t, `
templates:
  svc:
    params: [NAME]
    setup:
      - createdb ${NAME}_dev
      - seed ${NAME} --port ${PORT0}
    teardown:
      - dropdb ${NAME}_dev
projects:
  acme:
    template: svc
    params: { NAME: acme }
`)
	if err := expandTemplates(raw); err != nil {
		t.Fatalf("expandTemplates: %v", err)
	}
	acme := raw["projects"].(map[string]any)["acme"].(map[string]any)

	setup := acme["setup"].([]any)
	if got := setup[0]; got != "createdb acme_dev" {
		t.Errorf("setup[0] = %v; ${NAME} must be substituted inside the list", got)
	}
	if got := setup[1]; got != "seed acme --port ${PORT0}" {
		t.Errorf("setup[1] = %v; ${NAME} substituted but runtime ${PORT0} must pass through", got)
	}
	teardown := acme["teardown"].([]any)
	if got := teardown[0]; got != "dropdb acme_dev" {
		t.Errorf("teardown[0] = %v; ${NAME} must be substituted inside the list", got)
	}
}

func TestExpandNoTemplatesIsNoop(t *testing.T) {
	raw := rawTree(t, `{projects: {p: {repo: /r}}}`)
	if err := expandTemplates(raw); err != nil {
		t.Fatalf("config without templates must pass: %v", err)
	}
}

func TestExpandCollectsAllProjectErrors(t *testing.T) {
	raw := rawTree(t, `
templates:
  c:
    params: [NAME]
    repo: /r/${NAME}
projects:
  p1: {template: nope}
  p2: {template: c}
  p3: {repo: /r, params: {X: y}}
`)
	err := expandTemplates(raw)
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{
		`project "p1": unknown template "nope"`,
		`project "p2": missing param "NAME" of template "c"`,
		`project "p3": params without template`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("joined error should contain %q, got:\n%v", want, err)
		}
	}
}

func TestUsesTemplatesEmptyKeyIsFalse(t *testing.T) {
	for name, yml := range map[string]string{
		"null templates":  `{templates: ~, projects: {p: {repo: /r}}}`,
		"empty templates": `{templates: {}, projects: {p: {repo: /r}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if usesTemplates(rawTree(t, yml)) {
				t.Error("no project references a template — must be false (exact-position fast path)")
			}
		})
	}
}
