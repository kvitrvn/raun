package sqlite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kvitrvn/raun/internal/core/knowledge"
)

// timeLayout stores times as fixed-width text in UTC, so that the order of
// the text is the order of the times.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("stored time %q: %w", s, err)
	}
	return t.UTC(), nil
}

// document is the part of an item that is neither a column of knowledge_items
// nor its history. Its fields have a fixed order, so the same item always
// gives the same text. It is an internal representation: a change of its
// shape is a migration.
type document struct {
	Persona     *knowledge.Persona     `json:"persona,omitempty"`
	Requirement *knowledge.Requirement `json:"requirement,omitempty"`
	Support     []knowledge.Support    `json:"support,omitempty"`
	Evidence    []knowledge.Evidence   `json:"evidence,omitempty"`
	OpenPoints  []knowledge.OpenPoint  `json:"open_points,omitempty"`
}

func encodeDocument(it knowledge.Item) (string, error) {
	b, err := json.Marshal(document{
		Persona:     it.Persona,
		Requirement: it.Requirement,
		Support:     it.Support,
		Evidence:    it.Evidence,
		OpenPoints:  it.OpenPoints,
	})
	if err != nil {
		return "", fmt.Errorf("encode %s: %w", it.ID, err)
	}
	return string(b), nil
}

// decodeDocument fills it from a stored document. A field it does not know
// is an error: the document was written by another version, and reading it
// partly would lose content.
func decodeDocument(text string, it *knowledge.Item) error {
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.DisallowUnknownFields()
	var d document
	if err := dec.Decode(&d); err != nil {
		return fmt.Errorf("decode the document of %s: %w", it.ID, err)
	}
	if dec.More() {
		return fmt.Errorf("decode the document of %s: trailing data", it.ID)
	}
	it.Persona, it.Requirement = d.Persona, d.Requirement
	it.Support, it.Evidence, it.OpenPoints = d.Support, d.Evidence, d.OpenPoints
	return nil
}
