package config

import (
	"bytes"
	"errors"
	"io"

	"github.com/goccy/go-yaml"
)

// decodeStrict decodes YAML into Config, rejecting unknown keys with a
// positioned error. Strictness is the config contract (spec §4): a typo'd key
// is an error, not a silently ignored setting. An empty document decodes to a
// non-nil empty *Config.
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
