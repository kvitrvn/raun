package knowledge

import (
	"bytes"
	"fmt"

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
