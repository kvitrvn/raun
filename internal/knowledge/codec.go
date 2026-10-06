package knowledge

import (
	"bytes"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Encode serializes an item in canonical form: fixed field order, two-space
// indentation. Encoding a decoded canonical file yields identical bytes.
func Encode(it *Item) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(it); err != nil {
		return nil, fmt.Errorf("encode %s: %w", it.ID, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode %s: %w", it.ID, err)
	}
	return buf.Bytes(), nil
}

// Decode parses an item strictly (unknown fields rejected) and validates it.
func Decode(data []byte) (*Item, error) {
	var it Item
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&it); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if err := it.Validate(); err != nil {
		return nil, err
	}
	return &it, nil
}

// evidenceYAML is Evidence as written to disk. It exists only to control
// the style of the excerpt; its fields must mirror Evidence.
type evidenceYAML struct {
	ID        string        `yaml:"id"`
	Source    SourceKind    `yaml:"source"`
	Path      string        `yaml:"path"`
	Commit    string        `yaml:"commit"`
	StartLine int           `yaml:"start_line"`
	EndLine   int           `yaml:"end_line"`
	Excerpt   *yaml.Node    `yaml:"excerpt"`
	SHA256    string        `yaml:"sha256"`
	State     EvidenceState `yaml:"state"`
}

// MarshalYAML double-quotes multi-line excerpts whose first line starts
// with whitespace, such as code starting with a tab: the YAML library would
// write them as block scalars it cannot read back.
func (e Evidence) MarshalYAML() (any, error) {
	excerpt := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: e.Excerpt}
	if strings.Contains(e.Excerpt, "\n") && (strings.HasPrefix(e.Excerpt, " ") || strings.HasPrefix(e.Excerpt, "\t")) {
		excerpt.Style = yaml.DoubleQuotedStyle
	}
	return evidenceYAML{
		ID: e.ID, Source: e.Source, Path: e.Path, Commit: e.Commit,
		StartLine: e.StartLine, EndLine: e.EndLine, Excerpt: excerpt,
		SHA256: e.SHA256, State: e.State,
	}, nil
}
