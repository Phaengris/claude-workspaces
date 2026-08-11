package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestDecodeFullSchema(t *testing.T) {
	yml := `
values:
  PORT: { start: 5000, per_workspace: 10 }
env:
  RAILS_ENV: development
env_allow: [MY_TOKEN]
projects:
  my-app:
    repo: ~/dev/my-app
    base_branch: main
    depends: other
    setup: [bundle install]
    start:
      - echo waiting
      - rails: bin/rails s -p ${PORT0}
    # teardown/env are block style, not flow style, on purpose: goccy rejects
    # ${...} tokens inside flow collections [...] / {...} (the braces are flow
    # indicators), so do not "simplify" these back to flow style.
    teardown:
      - dropdb --if-exists ${DB_NAME}
    env:
      DB_NAME: my-app_${WORKSPACE}_development
    browse_port: ${PORT0}
  other:
    repo: ~/dev/other
    depends: []
`
	cfg, err := decodeStrict([]byte(yml))
	if err != nil {
		t.Fatalf("decodeStrict: %v", err)
	}
	if got := cfg.Values["PORT"]; got.Start != 5000 || got.PerWorkspace != 10 {
		t.Errorf("Values[PORT] = %+v", got)
	}
	app := cfg.Projects["my-app"]
	if app == nil {
		t.Fatal("missing project my-app")
	}
	if want := (StringList{"other"}); len(app.Depends) != 1 || app.Depends[0] != "other" {
		t.Errorf("Depends = %v, want %v", app.Depends, want)
	}
	wantStart := []StartEntry{{Cmd: "echo waiting"}, {Name: "rails", Cmd: "bin/rails s -p ${PORT0}"}}
	if len(app.Start) != 2 || app.Start[0] != wantStart[0] || app.Start[1] != wantStart[1] {
		t.Errorf("Start = %v, want %v", app.Start, wantStart)
	}
}

func TestDecodeRejectsUnknownKeys(t *testing.T) {
	yml := `
projects:
  my-app:
    repo: ~/dev/my-app
    comand_runner: rbenv exec
`
	_, err := decodeStrict([]byte(yml))
	if err == nil {
		t.Fatal("unknown key must be an error (strict decode)")
	}
	if !strings.Contains(err.Error(), "comand_runner") {
		t.Errorf("error should name the offending key, got: %v", err)
	}
}

// TestDecodeStrictnessSurvivesInlining guards the `document` wrapper (which
// inlines Config to accept and discard a leftover `templates:` key): inlining
// must not soften strict decode. An unknown TOP-LEVEL key and a duplicate key
// are both still errors, and `templates:` is tolerated only while empty.
func TestDecodeStrictnessSurvivesInlining(t *testing.T) {
	cases := map[string]struct {
		yml      string
		wantErr  bool
		wantName string
	}{
		"unknown top-level key": {yml: "porjects: {}\n", wantErr: true, wantName: "porjects"},
		"duplicate key":         {yml: "values: {}\nvalues: {}\n", wantErr: true, wantName: "values"},
		"empty templates key":   {yml: "templates: {}\nprojects: {p: {repo: /r}}\n"},
		"null templates key":    {yml: "templates: ~\nprojects: {p: {repo: /r}}\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeStrict([]byte(tc.yml))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error for %q", tc.yml)
				}
				if !strings.Contains(err.Error(), tc.wantName) {
					t.Errorf("error should name %q, got: %v", tc.wantName, err)
				}
				return
			}
			if err != nil {
				t.Errorf("an empty templates block must decode: %v", err)
			}
		})
	}
}

func TestStartEntryRejectsMultiKeyMap(t *testing.T) {
	yml := `
projects:
  p:
    repo: /r
    start:
      - rails: a
        vite: b
`
	if _, err := decodeStrict([]byte(yml)); err == nil {
		t.Fatal("a multi-key start map is ambiguous and must be an error")
	}
}

func TestStartEntryNestedForm(t *testing.T) {
	// Successful shapes: bare string, {name: cmd}, {name: {command,
	// description}}, and nested form WITHOUT description.
	yml := `
projects:
  app:
    repo: /tmp/x
    start:
      - bundle install
      - worker: bin/sidekiq
      - rails:
          command: bin/rails s -p ${PORT0}
          description: app server — UI at http://localhost:${PORT0}
      - quiet:
          command: sleep 30
`
	cfg, err := decodeStrict([]byte(yml))
	if err != nil {
		t.Fatalf("decodeStrict: %v", err)
	}
	got := cfg.Projects["app"].Start
	want := []StartEntry{
		{Cmd: "bundle install"},
		{Name: "worker", Cmd: "bin/sidekiq"},
		{Name: "rails", Cmd: "bin/rails s -p ${PORT0}",
			Description: "app server — UI at http://localhost:${PORT0}"},
		{Name: "quiet", Cmd: "sleep 30"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestStartEntryNestedFormErrors(t *testing.T) {
	cases := []struct{ name, yml, wantErr string }{
		{"missing command",
			"projects:\n  app:\n    repo: /tmp/x\n    start:\n      - rails:\n          description: d\n",
			"command"},
		{"unknown key",
			"projects:\n  app:\n    repo: /tmp/x\n    start:\n      - rails:\n          command: c\n          descriptoin: d\n",
			"descriptoin"},
		{"two names",
			"projects:\n  app:\n    repo: /tmp/x\n    start:\n      - rails: a\n        vite: b\n",
			"single"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeStrict([]byte(tc.yml))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestStringListAcceptsScalar(t *testing.T) {
	for _, tc := range []struct {
		yaml string
		want int
	}{
		{`{projects: {p: {repo: /r, depends: a}}}`, 1},
		{`{projects: {p: {repo: /r, depends: [a, b]}}}`, 2},
	} {
		cfg, err := decodeStrict([]byte(tc.yaml))
		if err != nil {
			t.Fatalf("%s: %v", tc.yaml, err)
		}
		if got := len(cfg.Projects["p"].Depends); got != tc.want {
			t.Errorf("%s: len(Depends) = %d, want %d", tc.yaml, got, tc.want)
		}
	}
}

func TestDecodeEmptyConfig(t *testing.T) {
	cfg, err := decodeStrict(nil)
	if err != nil {
		t.Fatalf("empty config must decode: %v", err)
	}
	if cfg == nil {
		t.Fatal("empty config must yield a non-nil *Config")
	}
}
