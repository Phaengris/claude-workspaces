package config

import (
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
