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

// UnmarshalYAML accepts `cmd` or `{name: cmd}`. A map with any count other
// than one key is ambiguous and is rejected as an error.
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

// UnmarshalYAML accepts a bare scalar as a one-element list or a native
// sequence as itself.
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
