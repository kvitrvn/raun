package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const answer = `{"version": 1, "items": [{"id": "k1", "title": "a"}, {"id": "k2", "title": "b"}]}`

func TestExtract(t *testing.T) {
	quoted := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	tests := []struct {
		name    string
		out     string
		salvage bool
		want    string
	}{
		{"bare", answer, false, answer},
		{"versioned object preferred over a later one", answer + "\n{\"usage\": 3}", false, answer},
		{"invalid escape repaired", `{"version": 1, "s": "matches \d+ in C:\Users"}`, false, `{"version": 1, "s": "matches \\d+ in C:\\Users"}`},
		{"control characters repaired", "{\"version\": 1, \"s\": \"a\nb\tc\x01\"}", false, `{"version": 1, "s": "a\nb\tc\u0001"}`},
		{"valid escapes kept", `{"version": 1, "s": "\"\\\/\b\f\n\r\t\u00e9"}`, false, `{"version": 1, "s": "\"\\\/\b\f\n\r\t\u00e9"}`},
		{"envelope", `{"type": "result", "result": ` + quoted("Done:\n```json\n"+answer+"\n```") + `}`, false, answer},
		{"nested envelope", `{"a": {"b": [` + quoted(`{"text": `+quoted(answer)+`}`) + `]}}`, false, answer},
		{"json lines", "{\"event\": \"start\"}\n{\"event\": \"text\", \"text\": " + quoted(answer) + "}\n{\"event\": \"end\"}\n", false, answer},
		{"last answer of a stream wins", `{"t": ` + quoted(`{"version": 0}`) + "}\n{\"t\": " + quoted(answer) + "}", false, answer},
		{"truncated in an element", answer[:strings.Index(answer, `"b"`)], true, `{"version": 1, "items": [{"id": "k1", "title": "a"}]}`},
		{"truncated in a string", answer[:strings.Index(answer, `"b"`)+2], true, `{"version": 1, "items": [{"id": "k1", "title": "a"}]}`},
		{"truncated after an array opens", `{"version": 1, "items": [`, true, `{"version": 1, "items": []}`},
		{"complete answer preferred to salvage", answer + "\n{\"version\": 2, \"items\": [", true, answer},
		{"truncated, not salvaged", "log " + answer[:40], false, ""},
		{"no versioned object: last one", `{"a": 1} {"a": 2}`, false, `{"a": 2}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Extract([]byte(tt.out), tt.salvage)
			if tt.want == "" {
				if !errors.Is(err, ErrNoJSON) {
					t.Errorf("Extract() = %s, %v; want ErrNoJSON", got, err)
				}
				return
			}
			if err != nil || string(got) != tt.want {
				t.Errorf("Extract() = %s, %v\nwant %s", got, err, tt.want)
			}
			if !json.Valid(got) {
				t.Errorf("Extract() returned invalid JSON: %s", got)
			}
		})
	}
}

// FuzzExtract checks that Extract never panics and only returns valid
// JSON objects, whatever an agent prints.
func FuzzExtract(f *testing.F) {
	for _, seed := range []string{
		answer, answer[:30], `{"version": 1, "s": "\d"}`, "{\"version\": 1, \"s\": \"\x00\"}",
		`{"r": "{\"version\": 1}"}`, `{"version": [{"a": "\`, "{{{{", `}{"version"`, "",
	} {
		f.Add([]byte(seed), true)
		f.Add([]byte(seed), false)
	}
	f.Fuzz(func(t *testing.T, out []byte, salvage bool) {
		got, err := Extract(out, salvage)
		if err != nil {
			return
		}
		if !json.Valid(got) || got[0] != '{' {
			t.Errorf("Extract(%q) = %q, not a JSON object", out, got)
		}
	})
}
