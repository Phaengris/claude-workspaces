# M0 Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Repo scaffold, config loading (strict YAML + templates + validation), the allocations registry, and environment curation — ending with `workspace doctor` validating the user's real `config.yml`.

**Architecture:** Per the design spec (`docs/superpowers/specs/2026-07-30-claude-workspaces-go-design.md`): thin `cmd/workspace/main.go` over an `internal/cli` cobra tree; leaf packages `config`, `alloc`, `envx`, `xerr` with no dependencies on each other except `alloc → config` (for `Value`) and everything → `xerr`. Config loads via raw-tree template expansion, then strict typed decode. State lives in `<root>/.allocations.json` under flock.

**Tech Stack:** Go (current stable), `spf13/cobra`, `goccy/go-yaml`, `golang.org/x/sys/unix`; dev-only `rogpeppe/go-internal/testscript`.

**This is plan 1 of 6** (one per spec milestone M0–M5). Later milestones get their own plans after this one is implemented and reviewed.

## Global Constraints

- Module path: `git.internal/cat/claude-workspaces-go`. Binary name: `workspace`.
- Runtime dependencies: ONLY `github.com/spf13/cobra`, `github.com/goccy/go-yaml`, `golang.org/x/sys`, `golang.org/x/term`. Dev-only: `github.com/rogpeppe/go-internal`. Adding anything else is a plan violation.
- Builds with `CGO_ENABLED=0`. POSIX-only; no Windows paths/APIs.
- Registry file: `<root>/.allocations.json`; lock file: `<root>/.lock`. Timestamps RFC 3339.
- Root dir: `$CLAUDE_WORKSPACES_ROOT_DIR`, else `~/claude-workspaces`.
- Exit codes: 0 success, 1 failure, 2 usage, 3 not found, 4 config error.
- Config decoding is STRICT: unknown YAML keys are errors.
- Every exported identifier gets a doc comment (this is a learning codebase — comments explain constraints, not narration).
- Before every commit: `gofmt -l .` reports nothing, `go vet ./...` clean, `go test ./...` green.
- Commit messages: conventional style (`feat(config): …`), trailer `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.
- This is a learning project: each task carries a **Learning** block (Go concepts it introduces) and a **Complexity** rating with a model recommendation. Implementers must keep code idiomatic — when a v1 Ruby idea has a more Go-natural shape, prefer the Go shape.

---

### Task 1: Scaffold, root command, exit-code mapping

**Files:**
- Create: `go.mod`, `cmd/workspace/main.go`, `internal/cli/root.go`, `internal/xerr/xerr.go`
- Test: `internal/xerr/xerr_test.go`, `internal/cli/root_test.go`
- Create: `.gitignore` (`/workspace` binary, `*.test`)

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `cli.Main() int` (run root command, map error → exit code); `cli.Root() *cobra.Command`; `xerr.ErrUsage`, `xerr.ErrNotFound`, `xerr.ErrConfig` sentinels; `xerr.Wrap(kind, err error) error`; `xerr.ExitCode(err error) int`.

**Learning:** Go modules (`go mod init`, `go.sum`), package layout and `internal/` visibility, sentinel errors + `errors.Is` + `%w` wrapping (incl. Go ≥1.20 multi-`%w`), cobra basics, table-driven tests.

**Complexity:** low — Opus.

- [ ] **Step 1: Init module and write the failing xerr test**

```bash
cd /home/cat/dev/claude-workspaces-go && go mod init git.internal/cat/claude-workspaces-go
```

`internal/xerr/xerr_test.go`:

```go
package xerr_test

import (
	"errors"
	"fmt"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

func TestExitCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"generic", errors.New("boom"), 1},
		{"usage", xerr.Wrap(xerr.ErrUsage, errors.New("bad args")), 2},
		{"not found", xerr.Wrap(xerr.ErrNotFound, errors.New("no ws")), 3},
		{"config", xerr.Wrap(xerr.ErrConfig, errors.New("bad yaml")), 4},
		{"deeply wrapped", fmt.Errorf("outer: %w", xerr.Wrap(xerr.ErrConfig, errors.New("inner"))), 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := xerr.ExitCode(tc.err); got != tc.want {
				t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestWrapKeepsMessage(t *testing.T) {
	err := xerr.Wrap(xerr.ErrConfig, errors.New("line 3: unknown key"))
	if !errors.Is(err, xerr.ErrConfig) {
		t.Fatal("wrapped error must satisfy errors.Is(err, ErrConfig)")
	}
	if want := "config: line 3: unknown key"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/xerr/`
Expected: FAIL (package does not exist / undefined identifiers).

- [ ] **Step 3: Implement xerr**

`internal/xerr/xerr.go`:

```go
// Package xerr defines the tool's error kinds. main maps them to exit codes,
// so scripts and the Claude skill can distinguish usage errors from missing
// workspaces from config problems (spec §9).
package xerr

import (
	"errors"
	"fmt"
)

var (
	ErrUsage    = errors.New("usage")
	ErrNotFound = errors.New("not found")
	ErrConfig   = errors.New("config")
)

// Wrap attaches a kind to err. Both remain matchable via errors.Is.
func Wrap(kind, err error) error {
	return fmt.Errorf("%w: %w", kind, err)
}

// ExitCode maps an error chain to the process exit code (spec §9).
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrUsage):
		return 2
	case errors.Is(err, ErrNotFound):
		return 3
	case errors.Is(err, ErrConfig):
		return 4
	default:
		return 1
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/xerr/`
Expected: PASS

- [ ] **Step 5: Write the failing root-command test**

`internal/cli/root_test.go`:

```go
package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionFlag(t *testing.T) {
	root := Root()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--version: %v", err)
	}
	if !strings.Contains(out.String(), "dev") {
		t.Errorf("version output %q should contain default version %q", out.String(), "dev")
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `go test ./internal/cli/`
Expected: FAIL (undefined: Root).

- [ ] **Step 7: Implement root command and main**

`internal/cli/root.go`:

```go
// Package cli builds the workspace command tree.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// version is stamped at release time via -ldflags "-X …/internal/cli.version=v…".
var version = "dev"

// Root builds the command tree. A fresh tree per call keeps tests independent.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:           "workspace",
		Short:         "Isolated, runnable dev-stack instances for parallel Claude Code sessions",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	return root
}

// Main runs the CLI and returns the process exit code. It is the single exit
// point: commands return errors (wrapped with an xerr kind where specific),
// nothing below main prints-and-dies.
func Main() int {
	err := Root().Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace:", err)
	}
	return xerr.ExitCode(err)
}
```

`cmd/workspace/main.go`:

```go
package main

import (
	"os"

	"git.internal/cat/claude-workspaces-go/internal/cli"
)

func main() {
	os.Exit(cli.Main())
}
```

Run `go get github.com/spf13/cobra@latest && go mod tidy`.

- [ ] **Step 8: Run tests + build to verify**

Run: `go test ./... && CGO_ENABLED=0 go build -o ./workspace ./cmd/workspace && ./workspace --version`
Expected: tests PASS, build succeeds, prints `workspace version dev`.

- [ ] **Step 9: Commit**

```bash
git add -A && git commit -m "feat: scaffold module, root command, exit-code mapping"
```

---

### Task 2: Config types + strict decode + custom unmarshalers

**Files:**
- Create: `internal/config/types.go`, `internal/config/decode.go`
- Test: `internal/config/decode_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: types `config.Config{Values map[string]Value, Env map[string]string, EnvAllow []string, Projects map[string]*Project}`, `config.Value{Start, PerWorkspace int}`, `config.Project{Repo, BaseBranch, Path string, Depends StringList, Setup []string, Start []StartEntry, Stop, Teardown []string, Env map[string]string, EnvAllow []string, BrowsePort, Instructions string}`, `config.StartEntry{Name, Cmd string}` (empty `Name` = run-and-wait command), `config.StringList []string`; function `decodeStrict(data []byte) (*Config, error)` (package-private; `Load` in Task 4 is the public entry).

**Learning:** struct tags and how decoders use them, pointer receivers, custom unmarshaling via the `UnmarshalYAML(unmarshal func(any) error) error` interface (the Go answer to Ruby duck-typed `case`), strict decoding options, YAML 1.1 footguns (`no` → bool) and why typed targets neutralize them.

**Complexity:** medium — Opus.

- [ ] **Step 1: Write the failing decode tests**

`internal/config/decode_test.go`:

```go
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
    teardown: [dropdb --if-exists ${DB_NAME}]
    env: { DB_NAME: my-app_${WORKSPACE}_development }
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/`
Expected: FAIL (undefined types).

- [ ] **Step 3: Implement types and strict decode**

`internal/config/types.go`:

```go
// Package config loads and validates <root>/config.yml (spec §4).
package config

import "fmt"

// Config is the decoded, template-expanded, validated configuration.
type Config struct {
	Values   map[string]Value    `yaml:"values"`
	Env      map[string]string   `yaml:"env"`
	EnvAllow []string            `yaml:"env_allow"`
	Projects map[string]*Project `yaml:"projects"`
}

// Value derives per-workspace numbers from the allocation index:
// workspace i gets NAME0..NAME(PerWorkspace-1) = Start+i*PerWorkspace + n.
type Value struct {
	Start        int `yaml:"start"`
	PerWorkspace int `yaml:"per_workspace"`
}

// Project is one configured project. Template/params keys never reach this
// struct — expansion consumes them on the raw tree before strict decode.
type Project struct {
	Repo         string            `yaml:"repo"`
	BaseBranch   string            `yaml:"base_branch"`
	Path         string            `yaml:"path"`
	Depends      StringList        `yaml:"depends"`
	Setup        []string          `yaml:"setup"`
	Start        []StartEntry      `yaml:"start"`
	Stop         []string          `yaml:"stop"`
	Teardown     []string          `yaml:"teardown"`
	Env          map[string]string `yaml:"env"`
	EnvAllow     []string          `yaml:"env_allow"`
	BrowsePort   string            `yaml:"browse_port"`
	Instructions string            `yaml:"instructions"`
}

// StartEntry is one `start:` item: a bare string is a run-and-wait command
// (Name == ""), a single-key map is a named daemon (spec §4).
type StartEntry struct {
	Name string
	Cmd  string
}

// UnmarshalYAML accepts `cmd` or `{name: cmd}`.
func (s *StartEntry) UnmarshalYAML(unmarshal func(any) error) error {
	var str string
	if err := unmarshal(&str); err == nil {
		s.Cmd = str
		return nil
	}
	var m map[string]string
	if err := unmarshal(&m); err != nil {
		return err
	}
	if len(m) != 1 {
		return fmt.Errorf("start entry must be a string or a single {name: command} pair, got %d keys", len(m))
	}
	for name, cmd := range m {
		s.Name, s.Cmd = name, cmd
	}
	return nil
}

// StringList accepts a scalar or a sequence: `depends: a` == `depends: [a]`.
type StringList []string

func (l *StringList) UnmarshalYAML(unmarshal func(any) error) error {
	var one string
	if err := unmarshal(&one); err == nil {
		*l = StringList{one}
		return nil
	}
	var many []string
	if err := unmarshal(&many); err != nil {
		return err
	}
	*l = many
	return nil
}
```

`internal/config/decode.go`:

```go
package config

import (
	"bytes"
	"errors"
	"io"

	"github.com/goccy/go-yaml"
)

// decodeStrict decodes YAML into Config, rejecting unknown and duplicate keys.
// Strictness is the config contract (spec §4): a typo'd key is an error with
// a position, not a silently ignored setting.
func decodeStrict(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data), yaml.Strict())
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return &Config{}, nil // empty file = empty config
		}
		return nil, err
	}
	return &cfg, nil
}
```

Run `go get github.com/goccy/go-yaml@latest && go mod tidy`.

Note for the implementer: goccy's option name for strict decoding is `yaml.Strict()` (combines disallow-unknown-field and disallow-duplicate-key). If the API differs in the fetched version, check `go doc github.com/goccy/go-yaml Strict` and use the equivalent `DecodeOption` — do not silently drop strictness.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat(config): schema types + strict decode + start-entry/string-list unmarshalers"
```

---

### Task 3: Template expansion on the raw tree

**Files:**
- Create: `internal/config/expand.go`
- Test: `internal/config/expand_test.go`

**Interfaces:**
- Consumes: nothing from other tasks (operates on `map[string]any` before typed decode).
- Produces: `expandTemplates(raw map[string]any) error` — resolves `templates:`/`template:`/`params:` in place, deletes the `templates` key. Task 4's `Load` calls it between raw unmarshal and `decodeStrict`.

**Learning:** working with `any` trees, type switches, recursion over heterogeneous data, building good error messages with `fmt.Errorf` + `%q`.

**Complexity:** medium — Opus. (Fiddly rules, but fully specified and table-testable.)

- [ ] **Step 1: Write the failing expansion tests**

`internal/config/expand_test.go`:

```go
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
    env: { CUSTOMER: "${NAME}-${WORKSPACE}" }
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run TestExpand`
Expected: FAIL (undefined: expandTemplates).

- [ ] **Step 3: Implement expansion**

`internal/config/expand.go`:

```go
package config

import (
	"fmt"
	"sort"
	"strings"
)

// expandTemplates resolves `templates:` on the raw YAML tree, in place,
// before strict typed decoding (spec §4). Load-time `${PARAM}` substitution
// covers only names declared in the template's params list; runtime tokens
// (${WORKSPACE}, ${PORT0}, …) pass through untouched.
func expandTemplates(raw map[string]any) error {
	templates, _ := raw["templates"].(map[string]any)
	defer delete(raw, "templates")

	projects, _ := raw["projects"].(map[string]any)
	for name, pAny := range projects {
		project, ok := pAny.(map[string]any)
		if !ok {
			continue // scalar project value; strict decode will reject it with a position
		}
		tmplName, usesTemplate := project["template"].(string)
		if !usesTemplate {
			if _, hasParams := project["params"]; hasParams {
				return fmt.Errorf("project %q: params without template", name)
			}
			continue
		}
		tmpl, ok := templates[tmplName].(map[string]any)
		if !ok {
			return fmt.Errorf("project %q: unknown template %q", name, tmplName)
		}
		merged, err := instantiate(name, tmplName, tmpl, project)
		if err != nil {
			return err
		}
		projects[name] = merged
	}
	return nil
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/`
Expected: PASS (all config tests, including Task 2's).

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat(config): template expansion on the raw tree"
```

---

### Task 4: Load — root resolution, tilde expansion, validation

**Files:**
- Create: `internal/config/load.go`, `internal/config/validate.go`
- Test: `internal/config/load_test.go`, `internal/config/validate_test.go`

**Interfaces:**
- Consumes: `decodeStrict` (Task 2), `expandTemplates` (Task 3), `xerr` (Task 1).
- Produces: `config.RootDir() (string, error)`; `config.Load(root string) (*Config, error)` (read → expand → strict decode → tilde-expand repos → validate; all errors wrapped `xerr.ErrConfig`); `config.ExpandTilde(path string) string`.

**Learning:** `os.UserHomeDir`/`filepath`, collecting multiple failures with `errors.Join`, Kahn's algorithm for cycle detection, wrapping errors with kinds across package boundaries.

**Complexity:** medium — Opus.

- [ ] **Step 1: Write the failing validate tests**

`internal/config/validate_test.go`:

```go
package config

import (
	"strings"
	"testing"
)

func TestValidateCollectsAllErrors(t *testing.T) {
	cfg := &Config{
		Values: map[string]Value{"PORT": {Start: 0, PerWorkspace: 0}},
		Projects: map[string]*Project{
			"a": {Depends: StringList{"ghost"}},
		},
	}
	err := cfg.validate()
	if err == nil {
		t.Fatal("want validation errors")
	}
	for _, want := range []string{
		`project "a": repo is required`,
		`project "a": depends on unknown project "ghost"`,
		`value "PORT": start must be positive`,
		`value "PORT": per_workspace must be at least 1`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should contain %q, got:\n%v", want, err)
		}
	}
}

func TestValidateDetectsDependencyCycle(t *testing.T) {
	cfg := &Config{Projects: map[string]*Project{
		"a": {Repo: "/r", Depends: StringList{"b"}},
		"b": {Repo: "/r", Depends: StringList{"a"}},
	}}
	err := cfg.validate()
	if err == nil || !strings.Contains(err.Error(), "dependency cycle") {
		t.Errorf("want dependency cycle error, got %v", err)
	}
}

func TestValidateOK(t *testing.T) {
	cfg := &Config{Projects: map[string]*Project{
		"a": {Repo: "/r", Depends: StringList{"b"}},
		"b": {Repo: "/r"},
	}}
	if err := cfg.validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run TestValidate`
Expected: FAIL (undefined: validate).

- [ ] **Step 3: Implement validation**

`internal/config/validate.go`:

```go
package config

import (
	"errors"
	"fmt"
	"sort"
)

// validate reports every problem at once (errors.Join), not just the first —
// doctor prints the full list (spec §2).
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

// checkCycles runs Kahn's algorithm over the depends graph; whatever cannot
// be topologically ordered is part of a cycle.
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -run TestValidate`
Expected: PASS

- [ ] **Step 5: Write the failing Load tests**

`internal/config/load_test.go`:

```go
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

func writeConfig(t *testing.T, yml string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadExpandsTildeInRepo(t *testing.T) {
	root := writeConfig(t, "projects: {p: {repo: ~/dev/p}}")
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, "dev/p"); cfg.Projects["p"].Repo != want {
		t.Errorf("Repo = %q, want %q", cfg.Projects["p"].Repo, want)
	}
}

func TestLoadWrapsErrorsAsConfig(t *testing.T) {
	for name, yml := range map[string]string{
		"bad yaml":       "projects: [::",
		"unknown key":    "projects: {p: {repo: /r, comand: x}}",
		"bad template":   "projects: {p: {template: nope}}",
		"invalid config": "projects: {p: {}}",
	} {
		t.Run(name, func(t *testing.T) {
			root := writeConfig(t, yml)
			_, err := Load(root)
			if !errors.Is(err, xerr.ErrConfig) {
				t.Errorf("Load error must wrap xerr.ErrConfig, got %v", err)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(t.TempDir())
	if !errors.Is(err, xerr.ErrConfig) || !strings.Contains(err.Error(), "config.yml") {
		t.Errorf("missing config.yml must be an ErrConfig naming the file, got %v", err)
	}
}

func TestRootDirEnvOverride(t *testing.T) {
	t.Setenv("CLAUDE_WORKSPACES_ROOT_DIR", "/custom/root")
	got, err := RootDir()
	if err != nil || got != "/custom/root" {
		t.Errorf("RootDir() = %q, %v; want /custom/root", got, err)
	}
}

func TestRootDirDefault(t *testing.T) {
	t.Setenv("CLAUDE_WORKSPACES_ROOT_DIR", "")
	home, _ := os.UserHomeDir()
	got, err := RootDir()
	if err != nil || got != filepath.Join(home, "claude-workspaces") {
		t.Errorf("RootDir() = %q, %v; want %q", got, err, filepath.Join(home, "claude-workspaces"))
	}
}
```

- [ ] **Step 6: Run tests to verify they fail**

Run: `go test ./internal/config/ -run 'TestLoad|TestRootDir'`
Expected: FAIL (undefined: Load, RootDir).

- [ ] **Step 7: Implement Load**

`internal/config/load.go`:

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"

	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// RootDir resolves the workspaces root: $CLAUDE_WORKSPACES_ROOT_DIR overrides
// ~/claude-workspaces. Every root is an independent universe with its own
// config and registry (spec §3).
func RootDir() (string, error) {
	if dir := os.Getenv("CLAUDE_WORKSPACES_ROOT_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	return filepath.Join(home, "claude-workspaces"), nil
}

// Load reads <root>/config.yml: raw unmarshal → template expansion → strict
// typed decode → tilde expansion → validation. Any failure is xerr.ErrConfig
// (exit code 4). Strict decode runs AFTER expansion because template/params
// keys are consumed by it and are not part of the typed schema.
func Load(root string) (*Config, error) {
	path := filepath.Join(root, "config.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, xerr.Wrap(xerr.ErrConfig, err)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, xerr.Wrap(xerr.ErrConfig, err)
	}
	if raw == nil {
		return &Config{}, nil
	}
	if err := expandTemplates(raw); err != nil {
		return nil, xerr.Wrap(xerr.ErrConfig, err)
	}
	clean, err := yaml.Marshal(raw)
	if err != nil {
		return nil, xerr.Wrap(xerr.ErrConfig, err)
	}
	cfg, err := decodeStrict(clean)
	if err != nil {
		return nil, xerr.Wrap(xerr.ErrConfig, err)
	}
	for _, p := range cfg.Projects {
		if p != nil {
			p.Repo = ExpandTilde(p.Repo)
		}
	}
	if err := cfg.validate(); err != nil {
		return nil, xerr.Wrap(xerr.ErrConfig, err)
	}
	return cfg, nil
}

// ExpandTilde replaces a leading ~ with the home directory. Go has no
// built-in for this (the shell normally does it; config values bypass the
// shell) — v1's most-forgotten porting pitfall, so it lives in one place.
func ExpandTilde(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./internal/config/`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add -A && git commit -m "feat(config): Load with root resolution, tilde expansion, joined validation"
```

---

### Task 5: Allocations registry — flock, atomic save

**Files:**
- Create: `internal/alloc/registry.go`
- Test: `internal/alloc/registry_test.go`

**Interfaces:**
- Consumes: nothing (leaf package; `config.Value` arrives in Task 6).
- Produces: `alloc.Allocation{Index int, TaskID, Description, CreatedAt string, Adopted bool}` (JSON tags `index`, `task_id`, `description`, `created_at`, `adopted`); `alloc.Registry map[string]Allocation` (key: absolute workspace dir); `alloc.Load(root string) (Registry, error)` (missing file → empty registry); `alloc.Save(root string, r Registry) error` (temp file + fsync + rename); `alloc.WithLock(root string, fn func() error) error` (flock on `<root>/.lock`).

**Learning:** `defer` for cleanup ordering, `errors.Is` with `fs.ErrNotExist`, atomic write via `os.CreateTemp` + `Rename`, advisory locking with `unix.Flock`, first goroutine + channel (in the lock test).

**Complexity:** medium-high — Fable. (Locking and atomicity subtleties deserve the careful model.)

- [ ] **Step 1: Write the failing registry tests**

`internal/alloc/registry_test.go`:

```go
package alloc_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
)

func TestLoadMissingFileIsEmptyRegistry(t *testing.T) {
	reg, err := alloc.Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load on empty root: %v", err)
	}
	if len(reg) != 0 {
		t.Errorf("want empty registry, got %v", reg)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	root := t.TempDir()
	want := alloc.Registry{
		"/ws/FIZZY-123_fix": {Index: 0, TaskID: "FIZZY-123", Description: "fix", CreatedAt: "2026-07-30T12:00:00+03:00"},
		"/elsewhere/adopted": {Index: 2, TaskID: "ADHOC-1", Adopted: true, CreatedAt: "2026-07-30T13:00:00+03:00"},
	}
	if err := alloc.Save(root, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := alloc.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got["/ws/FIZZY-123_fix"] != want["/ws/FIZZY-123_fix"] || got["/elsewhere/adopted"] != want["/elsewhere/adopted"] {
		t.Errorf("roundtrip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestSaveIsHiddenFileWithTrailingNewline(t *testing.T) {
	root := t.TempDir()
	if err := alloc.Save(root, alloc.Registry{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".allocations.json"))
	if err != nil {
		t.Fatalf("registry must live at <root>/.allocations.json: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("registry file should end with a newline")
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			t.Errorf("Save must not leave visible files behind, found %q", e.Name())
		}
	}
}

func TestWithLockExcludesSecondLocker(t *testing.T) {
	root := t.TempDir()
	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- alloc.WithLock(root, func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked // the goroutine now holds the flock

	f, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// flock contends per open file description, so a second open in the same
	// process is a faithful stand-in for a second workspace process.
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Errorf("second locker should get EWOULDBLOCK while lock held, got %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Errorf("lock should be free after WithLock returns, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/alloc/`
Expected: FAIL (package does not exist).

- [ ] **Step 3: Implement the registry**

`internal/alloc/registry.go`:

```go
// Package alloc owns <root>/.allocations.json — the tool's ONLY registry
// (spec §3). Everything else about a workspace is derived from reality.
package alloc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	registryName = ".allocations.json"
	lockName     = ".lock"
)

// Allocation records the facts that cannot be derived by looking at a
// workspace dir: its index (the basis for PORT0…) and its identity.
type Allocation struct {
	Index       int    `json:"index"`
	TaskID      string `json:"task_id"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"` // RFC 3339
	Adopted     bool   `json:"adopted"`
}

// Registry maps absolute workspace dir → allocation.
type Registry map[string]Allocation

// Load reads the registry; a missing file is an empty registry, not an error
// (a fresh root has no allocations yet).
func Load(root string) (Registry, error) {
	data, err := os.ReadFile(filepath.Join(root, registryName))
	if errors.Is(err, fs.ErrNotExist) {
		return Registry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var reg Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", registryName, err)
	}
	return reg, nil
}

// Save writes the registry atomically: temp file in the same dir, fsync,
// rename. A reader never observes a half-written file, even without the lock.
func Save(root string, reg Registry) error {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(root, ".allocations-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(root, registryName))
}

// WithLock runs fn while holding an exclusive flock on <root>/.lock —
// the mutual exclusion for read-modify-write cycles across workspace
// processes. Advisory: correctness relies on every writer using WithLock.
func WithLock(root string, fn func() error) error {
	f, err := os.OpenFile(filepath.Join(root, lockName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("locking %s: %w", lockName, err)
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)

	return fn()
}
```

Run `go get golang.org/x/sys@latest && go mod tidy`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/alloc/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat(alloc): allocations registry with flock and atomic save"
```

---

### Task 6: Index assignment + values computation

**Files:**
- Create: `internal/alloc/values.go`
- Test: `internal/alloc/values_test.go`

**Interfaces:**
- Consumes: `alloc.Registry` (Task 5), `config.Value` (Task 2).
- Produces: `alloc.NextIndex(reg Registry) int` (lowest unused index, gap-filling); `alloc.ComputeValues(values map[string]config.Value, index int) map[string]string` (e.g. `PORT` start 5000 per_workspace 10, index 2 → `PORT0`=`5020` … `PORT9`=`5029`).

**Learning:** pure functions over maps, `strconv`, why gap-filling matters (released indices are reused so ports don't run away), cross-package type reuse.

**Complexity:** low — Opus.

- [ ] **Step 1: Write the failing tests**

`internal/alloc/values_test.go`:

```go
package alloc_test

import (
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/config"
)

func TestNextIndexFillsGaps(t *testing.T) {
	cases := []struct {
		name string
		used []int
		want int
	}{
		{"empty", nil, 0},
		{"contiguous", []int{0, 1, 2}, 3},
		{"gap", []int{0, 2, 3}, 1},
		{"zero freed", []int{1, 2}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := alloc.Registry{}
			for i, idx := range tc.used {
				reg[string(rune('a'+i))] = alloc.Allocation{Index: idx}
			}
			if got := alloc.NextIndex(reg); got != tc.want {
				t.Errorf("NextIndex(%v) = %d, want %d", tc.used, got, tc.want)
			}
		})
	}
}

func TestComputeValues(t *testing.T) {
	values := map[string]config.Value{
		"PORT":     {Start: 5000, PerWorkspace: 10},
		"REDIS_DB": {Start: 1, PerWorkspace: 1},
	}
	got := alloc.ComputeValues(values, 2)
	want := map[string]string{"PORT0": "5020", "PORT9": "5029", "REDIS_DB0": "3"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != 11 { // PORT0..PORT9 + REDIS_DB0
		t.Errorf("len = %d, want 11 (%v)", len(got), got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/alloc/ -run 'TestNextIndex|TestComputeValues'`
Expected: FAIL (undefined: NextIndex, ComputeValues).

- [ ] **Step 3: Implement**

`internal/alloc/values.go`:

```go
package alloc

import (
	"strconv"

	"git.internal/cat/claude-workspaces-go/internal/config"
)

// NextIndex returns the lowest index not currently allocated. Gap-filling —
// a released workspace's index (and thus its port block) is reused instead
// of growing forever.
func NextIndex(reg Registry) int {
	used := make(map[int]bool, len(reg))
	for _, a := range reg {
		used[a.Index] = true
	}
	for i := 0; ; i++ {
		if !used[i] {
			return i
		}
	}
}

// ComputeValues derives a workspace's numbered values from its index:
// NAME<n> = Start + index*PerWorkspace + n, for n in [0, PerWorkspace).
// Values are strings because they substitute into commands and env vars.
func ComputeValues(values map[string]config.Value, index int) map[string]string {
	out := make(map[string]string)
	for name, v := range values {
		base := v.Start + index*v.PerWorkspace
		for n := 0; n < v.PerWorkspace; n++ {
			out[name+strconv.Itoa(n)] = strconv.Itoa(base + n)
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/alloc/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat(alloc): gap-filling index assignment and values computation"
```

---

### Task 7: envx — curated environment

**Files:**
- Create: `internal/envx/envx.go`
- Test: `internal/envx/envx_test.go`

**Interfaces:**
- Consumes: nothing (leaf package; takes `[]string`/maps, never reads config types).
- Produces: `envx.SanitizePATH(path string) string`; `envx.Curated(parent []string, extraAllow []string, overlay map[string]string) []string` (parent in `os.Environ()` "K=V" form; returns the complete child env for `exec.Cmd.Env`); `envx.SanitizeSelf()` (mutates the process env at startup: sanitized PATH + version-manager pin vars deleted).

Behavioral oracle: v1 `env_tools.rb` documented behavior + `version_resolution_spec.rb` cases (spec §6). The allowlist and prefix list below are the contract, verbatim.

**Learning:** `strings.Cut`, building "K=V" slices and why `exec.Cmd.Env` is total (nothing inherited unless put there — the Go idiom that deletes v1's `unsetenv_others` footgun), slices vs maps for ordered output, `t.Setenv` for env-mutating tests.

**Complexity:** medium — Fable. (The logic is plain string filtering, but this is the tool's semantic core; fidelity to the v1 oracle is the point.)

- [ ] **Step 1: Write the failing tests**

`internal/envx/envx_test.go`:

```go
package envx_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/envx"
)

func TestSanitizePATH(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			"strips rbenv concrete version bin, keeps shims",
			"/home/u/.rbenv/versions/3.3.9/bin:/home/u/.rbenv/shims:/usr/bin",
			"/home/u/.rbenv/shims:/usr/bin",
		},
		{
			"strips asdf install bin",
			"/home/u/.asdf/installs/ruby/3.4.8/bin:/usr/bin",
			"/usr/bin",
		},
		{
			"keeps unrelated dirs",
			"/usr/local/bin:/usr/bin:/bin",
			"/usr/local/bin:/usr/bin:/bin",
		},
		{
			"trailing slash still stripped",
			"/home/u/.rbenv/versions/3.3.9/bin/:/usr/bin",
			"/usr/bin",
		},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := envx.SanitizePATH(tc.in); got != tc.want {
				t.Errorf("SanitizePATH(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCurated(t *testing.T) {
	parent := []string{
		"HOME=/home/u",
		"LANG=en_US.UTF-8",
		"RBENV_VERSION=3.3.9",          // version-manager pin: dropped
		"SECRET_TOKEN=shh",             // not allowlisted: dropped
		"MY_TOKEN=ok",                  // allowlisted via extraAllow
		"PATH=/home/u/.rbenv/versions/3.3.9/bin:/home/u/.rbenv/shims:/usr/bin",
	}
	overlay := map[string]string{"DB_NAME": "app_T1_development", "LANG": "C"}
	got := envx.Curated(parent, []string{"MY_TOKEN"}, overlay)

	want := map[string]string{
		"HOME":    "/home/u",
		"LANG":    "C", // overlay wins over parent
		"MY_TOKEN": "ok",
		"DB_NAME": "app_T1_development",
		"PATH":    "/home/u/.rbenv/shims:/usr/bin",
	}
	gotMap := map[string]string{}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		gotMap[k] = v
	}
	for k, v := range want {
		if gotMap[k] != v {
			t.Errorf("%s = %q, want %q", k, gotMap[k], v)
		}
	}
	for _, banned := range []string{"RBENV_VERSION", "SECRET_TOKEN"} {
		if _, ok := gotMap[banned]; ok {
			t.Errorf("%s must not propagate", banned)
		}
	}
	if !slices.IsSorted(got) {
		t.Error("Curated output should be sorted for deterministic behavior")
	}
}

func TestSanitizeSelf(t *testing.T) {
	t.Setenv("PATH", "/home/u/.rbenv/versions/3.3.9/bin:/usr/bin")
	t.Setenv("RBENV_VERSION", "3.3.9")
	t.Setenv("MISE_RUBY_VERSION", "3.4.8")
	t.Setenv("HOME", os.Getenv("HOME")) // untouched control

	envx.SanitizeSelf()

	if got := os.Getenv("PATH"); got != "/usr/bin" {
		t.Errorf("PATH = %q, want /usr/bin", got)
	}
	for _, k := range []string{"RBENV_VERSION", "MISE_RUBY_VERSION"} {
		if _, ok := os.LookupEnv(k); ok {
			t.Errorf("%s must be deleted by SanitizeSelf", k)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/envx/`
Expected: FAIL (package does not exist).

- [ ] **Step 3: Implement**

`internal/envx/envx.go`:

```go
// Package envx builds the curated environment for every spawned process
// (spec §6). The design is a documented compromise: an allowlist of safe
// vars + a sanitized PATH that keeps version-manager shims (per-directory
// dispatchers) but strips concrete per-version bins, so each worktree's own
// .ruby-version / .tool-versions resolves by cwd. Fail-safe: over-keeping a
// segment only reproduces pin-to-launch-version behavior, never worse.
package envx

import (
	"os"
	"slices"
	"strings"
)

// allowlist bounds the safe vars; anything not named here is dropped, so a
// new version manager's vars can never leak no matter what they're called.
// The user extends it per config with env_allow — never by editing this list.
var allowlist = []string{
	"HOME", "USER", "LOGNAME", "SHELL", "TERM", "TERM_PROGRAM",
	"LANG", "LANGUAGE", "LC_ALL", "LC_CTYPE", "LC_MESSAGES", "LC_COLLATE", "LC_NUMERIC", "LC_TIME",
	"TZ",
	"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY",
	"SSH_AUTH_SOCK", "SSH_AGENT_PID",
	"GPG_AGENT_INFO", "GNUPGHOME",
	"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
	"DBUS_SESSION_BUS_ADDRESS",
}

// versionManagerPrefixes covers the version-*selection* vector only: pin vars
// exported by shims into our process. Gem/module-path vars (RUBYOPT, GEM_HOME,
// PYTHONPATH, …) are simply not allowlisted — once selection is correct the
// shim re-derives them per command. New manager → add one prefix.
var versionManagerPrefixes = []string{
	"RBENV_", "PYENV_", "NODENV_", "PLENV_", "GOENV_", "RUBYENV_", "ASDF_", "MISE_", "__MISE_",
}

// SanitizePATH strips concrete per-version install bins — segments containing
// /versions/ or /installs/ and ending in /bin — leaving shims reachable.
func SanitizePATH(path string) string {
	if path == "" {
		return ""
	}
	segs := strings.Split(path, ":")
	kept := segs[:0]
	for _, seg := range segs {
		if !concreteVersionBin(seg) {
			kept = append(kept, seg)
		}
	}
	return strings.Join(kept, ":")
}

func concreteVersionBin(seg string) bool {
	if seg == "" {
		return false
	}
	trimmed := strings.TrimSuffix(seg, "/")
	return (strings.Contains(trimmed, "/versions/") || strings.Contains(trimmed, "/installs/")) &&
		strings.HasSuffix(trimmed, "/bin")
}

// Curated builds the complete child environment: allowlisted parent vars
// (plus extraAllow from config env_allow), sanitized PATH, overlay
// (workspace/project env) merged last so it always wins. Sorted for
// deterministic output. The result is assigned to exec.Cmd.Env, which is
// total — nothing else is inherited.
func Curated(parent []string, extraAllow []string, overlay map[string]string) []string {
	allowed := make(map[string]bool, len(allowlist)+len(extraAllow))
	for _, k := range allowlist {
		allowed[k] = true
	}
	for _, k := range extraAllow {
		allowed[k] = true
	}

	env := make(map[string]string)
	for _, kv := range parent {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch {
		case k == "PATH":
			env["PATH"] = SanitizePATH(v)
		case allowed[k] && !hasVersionManagerPrefix(k):
			env[k] = v
		}
	}
	for k, v := range overlay {
		env[k] = v
	}

	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

func hasVersionManagerPrefix(k string) bool {
	for _, p := range versionManagerPrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// SanitizeSelf undoes, in place, the version-manager activation that launched
// this process (PATH prepends + pin vars), so every later inherit-spawn
// (claude, exec) is clean by default. Called once at startup. Idempotent.
func SanitizeSelf() {
	if path, ok := os.LookupEnv("PATH"); ok {
		os.Setenv("PATH", SanitizePATH(path))
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if hasVersionManagerPrefix(k) {
			os.Unsetenv(k)
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/envx/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat(envx): curated spawn environment — allowlist, PATH sanitizer, self-sanitize"
```

---

### Task 8: doctor command + testscript harness

**Files:**
- Create: `internal/cli/doctor.go`, `internal/cli/cli_test.go`
- Create: `internal/cli/testdata/doctor_ok.txtar`, `internal/cli/testdata/doctor_bad.txtar`, `internal/cli/testdata/doctor_missing.txtar`
- Modify: `internal/cli/root.go` (register doctor; wire `envx.SanitizeSelf` into `PersistentPreRun`)

**Interfaces:**
- Consumes: `config.RootDir`, `config.Load` (Task 4), `alloc.Load` (Task 5), `envx.SanitizeSelf` (Task 7), `cli.Main` (Task 1).
- Produces: `workspace doctor` — prints a sorted config report, exit 0 when valid, exit 4 with the full error list otherwise. M0 scope: config + registry readability + allocation-dirs-exist check. (Orphan-daemon and port checks arrive with M5's "doctor full".)

**Learning:** `testscript` (txtar CLI tests — the harness the Go team tests `go` itself with), `TestMain`, wiring leaf packages into a command, `PersistentPreRun`, `tabwriter`-free simple output with `fmt.Fprintf(cmd.OutOrStdout(), …)` for testability.

**Complexity:** medium — Fable. (Harness wiring is fiddly; everything downstream builds on it.)

- [ ] **Step 1: Write the failing testscript harness + scripts**

`internal/cli/cli_test.go`:

```go
package cli_test

import (
	"os"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"git.internal/cat/claude-workspaces-go/internal/cli"
)

func TestMain(m *testing.M) {
	os.Exit(testscript.RunMain(m, map[string]func() int{
		"workspace": cli.Main,
	}))
}

func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{Dir: "testdata"})
}
```

`internal/cli/testdata/doctor_ok.txtar`:

```
# doctor on a valid config reports OK and the project list, sorted.
env CLAUDE_WORKSPACES_ROOT_DIR=$WORK/root
exec workspace doctor
stdout 'config: OK'
stdout 'projects \(2\): app-a, app-b'
stdout 'values: PORT = 10 per workspace from 5000'

-- root/config.yml --
values:
  PORT: { start: 5000, per_workspace: 10 }
projects:
  app-b:
    repo: /tmp/does-not-need-to-exist-in-m0
  app-a:
    repo: /tmp/also-fine
```

`internal/cli/testdata/doctor_bad.txtar`:

```
# doctor on an invalid config exits 4 and lists every problem.
env CLAUDE_WORKSPACES_ROOT_DIR=$WORK/root
! exec workspace doctor
stderr 'unknown'
stderr 'comand_runner'

-- root/config.yml --
projects:
  app:
    repo: /tmp/x
    comand_runner: rbenv exec
```

`internal/cli/testdata/doctor_missing.txtar`:

```
# doctor with no config.yml is a config error, not a crash.
env CLAUDE_WORKSPACES_ROOT_DIR=$WORK/root
mkdir $WORK/root
! exec workspace doctor
stderr 'config.yml'
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go get github.com/rogpeppe/go-internal@latest && go mod tidy && go test ./internal/cli/`
Expected: FAIL (`unknown command "doctor"`).

Note: `go mod tidy` keeps `rogpeppe/go-internal` as a test-only dependency — check `go.mod` marks it `// indirect`-free but it must NOT appear in the built binary (`go version -m` on the binary to verify, or trust that only `_test.go` files import it).

- [ ] **Step 3: Implement doctor + wire SanitizeSelf**

`internal/cli/doctor.go`:

```go
package cli

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/config"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Validate config.yml and check registry health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := config.RootDir()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err // ErrConfig → exit 4, message lists every problem
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "config: OK")

			names := make([]string, 0, len(cfg.Projects))
			for name := range cfg.Projects {
				names = append(names, name)
			}
			sort.Strings(names)
			fmt.Fprintf(out, "projects (%d): %s\n", len(names), joinOr(names, "none"))

			valNames := make([]string, 0, len(cfg.Values))
			for name := range cfg.Values {
				valNames = append(valNames, name)
			}
			sort.Strings(valNames)
			for _, name := range valNames {
				v := cfg.Values[name]
				fmt.Fprintf(out, "values: %s = %d per workspace from %d\n", name, v.PerWorkspace, v.Start)
			}

			reg, err := alloc.Load(root)
			if err != nil {
				return fmt.Errorf("registry: %w", err)
			}
			for _, dir := range sortedRegistryDirs(reg) {
				if _, err := os.Stat(dir); err != nil {
					fmt.Fprintf(out, "stale allocation: %s (dir missing — run `workspace gc`)\n", dir)
				}
			}
			return nil
		},
	}
}

func sortedRegistryDirs(reg alloc.Registry) []string {
	dirs := make([]string, 0, len(reg))
	for dir := range reg {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}

func joinOr(items []string, empty string) string {
	if len(items) == 0 {
		return empty
	}
	out := items[0]
	for _, s := range items[1:] {
		out += ", " + s
	}
	return out
}
```

In `internal/cli/root.go`, inside `Root()` before `return root`:

```go
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		envx.SanitizeSelf() // undo version-manager activation before anything spawns (spec §6)
	}
	root.AddCommand(newDoctorCmd())
```

(add imports `git.internal/cat/claude-workspaces-go/internal/envx`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS across all packages.

Note for the implementer: `envx.SanitizeSelf` in `PersistentPreRun` mutates the test process env under testscript. testscript already gives each script an isolated env, so this is safe — but if `TestVersionFlag` or others become env-sensitive, use `t.Setenv` to shield them.

- [ ] **Step 5: Try it on the real config**

Run: `CGO_ENABLED=0 go build -o ./workspace ./cmd/workspace && ./workspace doctor`
Expected: the M0 exit criterion — a truthful report on `~/claude-workspaces/config.yml`. If the real config uses a schema feature the plan missed, STOP and report the discrepancy rather than patching ad hoc — that's a spec conversation.

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "feat(cli): doctor command + testscript harness; sanitize env at startup"
```

---

## Self-review notes

- **Spec coverage (M0 slice):** §4 config (Tasks 2–4), §3 registry (Task 5, minus derived-state helpers that belong to M1+), §6 envx (Task 7; `env_allow` plumbed as `extraAllow` parameter, consumed by spawn sites in M2+), §9 layout/exit codes (Task 1), doctor-config subset (Task 8). Deliberately absent (later milestones): `wsp`, `proc`, `gitx`, `ui --json`, embed/install, WORKSPACE.md.
- **Type consistency:** `config.Value{Start, PerWorkspace}` used identically in Tasks 2/4/6/8; `Registry map[string]Allocation` in 5/6/8; `cli.Main() int` in 1/8.
- **Known simplifications, on purpose:** doctor's M0 output format is a draft (M5 owns the full report); `Curated` takes `parent []string` rather than reading `os.Environ()` so tests need no env mutation; per-project `env_allow` merging with global happens at spawn-site construction (M2), not in envx.
