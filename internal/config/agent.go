package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"unicode"

	"github.com/goccy/go-yaml"
)

// DefaultInstructionsFile is the instructions file name when an agent: block
// does not set one.
const DefaultInstructionsFile = "AGENTS.md"

// Agent is the `agent:` block: how chat starts and resumes the coding agent.
// The commands run as `$SHELL -lc <command>`; no ${...} substitution.
type Agent struct {
	Start            string `yaml:"start"`
	Resume           string `yaml:"resume"`
	CanResume        string `yaml:"can_resume"`
	InstructionsFile string `yaml:"instructions_file"`
}

// InstructionsFileName is the configured file name, or the default.
func (a *Agent) InstructionsFileName() string {
	if a.InstructionsFile == "" {
		return DefaultInstructionsFile
	}
	return a.InstructionsFile
}

// InstructionsFile is the instructions file name for this config: the block's,
// or the built-in Claude block's when no block is configured.
func (c *Config) InstructionsFile() string {
	if c.Agent != nil {
		return c.Agent.InstructionsFileName()
	}
	return ClaudeAgent().InstructionsFileName()
}

// validate reports every problem in the block, each prefixed "agent: ".
func (a *Agent) validate() []error {
	var errs []error
	if strings.TrimSpace(a.Start) == "" {
		errs = append(errs, fmt.Errorf("agent: start is required"))
	}
	if (strings.TrimSpace(a.Resume) == "") != (strings.TrimSpace(a.CanResume) == "") {
		errs = append(errs, fmt.Errorf("agent: resume and can_resume go together"))
	}
	if f := a.InstructionsFile; f != "" && (f == "." || f == ".." || strings.ContainsAny(f, `/\`) || strings.IndexFunc(f, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0) {
		errs = append(errs, fmt.Errorf("agent: instructions_file must be a plain file name (got %q)", f))
	}
	return errs
}

// claudeAgentYAML is the ONLY home of Claude-specific knowledge: the block
// chat falls back to when config.yml has no agent: block, and the text the
// config stub shows commented out.
//
//go:embed claude_agent.yml
var claudeAgentYAML []byte

// ClaudeAgentYAML returns a copy of the built-in block's text.
func ClaudeAgentYAML() []byte { return append([]byte(nil), claudeAgentYAML...) }

var claudeAgent = mustParseClaudeAgent()

func mustParseClaudeAgent() Agent {
	var doc struct {
		Agent *Agent `yaml:"agent"`
	}
	if err := yaml.NewDecoder(bytes.NewReader(claudeAgentYAML), yaml.Strict()).Decode(&doc); err != nil {
		panic("config: built-in claude_agent.yml: " + err.Error())
	}
	if doc.Agent == nil {
		panic("config: built-in claude_agent.yml has no agent: block")
	}
	if errs := doc.Agent.validate(); len(errs) > 0 {
		panic(fmt.Sprintf("config: built-in claude_agent.yml: %v", errs))
	}
	return *doc.Agent
}

// ClaudeAgent returns the parsed built-in Claude block, a fresh copy per call.
func ClaudeAgent() *Agent {
	a := claudeAgent
	return &a
}
