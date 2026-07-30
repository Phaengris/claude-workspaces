package config

import (
	"bytes"
	"errors"
	"io"

	"github.com/goccy/go-yaml"
)

// decodeStrict decodes YAML into Config, rejecting unknown and duplicate keys
// with a positioned error. Strictness is the config contract (spec §4): a
// typo'd or repeated key is an error, not a silently ignored/overwritten
// setting. Enforcement: yaml.Strict() supplies unknown-field rejection; goccy's
// parser rejects duplicate mapping keys unconditionally (independent of
// Strict()), so both halves hold — a future decoder/option change must preserve
// duplicate-key rejection too. An empty document decodes to a non-nil empty
// *Config.
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
