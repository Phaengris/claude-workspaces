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

func TestExpandNoTemplatesIsNoop(t *testing.T) {
	raw := rawTree(t, `{projects: {p: {repo: /r}}}`)
	if err := expandTemplates(raw); err != nil {
		t.Fatalf("config without templates must pass: %v", err)
	}
}
