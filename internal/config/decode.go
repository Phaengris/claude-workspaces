package config

import (
	"bytes"
	"errors"
	"fmt"
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
// A `templates:` key is consumed by expansion on the raw tree, so the typed
// schema has no home for it — yet an empty or null `templates:` block skips
// expansion entirely (usesTemplates says it declares nothing) and therefore
// still sits in the bytes handed to the decoder. document accepts and discards
// it so that case keeps the exact-position fast path instead of round-tripping
// just to strip a key, and so Config stays free of a field no caller wants.
type document struct {
	Config    `yaml:",inline"`
	Templates map[string]any `yaml:"templates"`
}

func decodeStrict(data []byte) (*Config, error) {
	var doc document
	dec := yaml.NewDecoder(bytes.NewReader(data), yaml.Strict())
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return &Config{}, nil // empty file = empty config
		}
		return nil, err
	}
	if len(doc.Templates) > 0 {
		// Unreachable via Load (a populated block routes through expansion,
		// which deletes the key), so this can only mean the two have drifted.
		return nil, fmt.Errorf("internal: templates block reached strict decode unexpanded")
	}
	cfg := doc.Config
	return &cfg, nil
}
