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
		err     error // when want is empty
	}{
		{"bare", answer, false, answer, nil},
		{"versioned object preferred over a later one", answer + "\n{\"usage\": 3}", false, answer, nil},
		{"invalid escape repaired", `{"version": 1, "s": "matches \d+ in C:\Users"}`, false, `{"version": 1, "s": "matches \\d+ in C:\\Users"}`, nil},
		{"control characters repaired", "{\"version\": 1, \"s\": \"a\nb\tc\x01\"}", false, `{"version": 1, "s": "a\nb\tc\u0001"}`, nil},
		{"valid escapes kept", `{"version": 1, "s": "\"\\\/\b\f\n\r\t\u00e9"}`, false, `{"version": 1, "s": "\"\\\/\b\f\n\r\t\u00e9"}`, nil},
		{"envelope", `{"type": "result", "result": ` + quoted("Done:\n```json\n"+answer+"\n```") + `}`, false, answer, nil},
		{"nested envelope", `{"a": {"b": [` + quoted(`{"text": `+quoted(answer)+`}`) + `]}}`, false, answer, nil},
		{"json lines", "{\"event\": \"start\"}\n{\"event\": \"text\", \"text\": " + quoted(answer) + "}\n{\"event\": \"end\"}\n", false, answer, nil},
		{"last answer of a stream wins", `{"t": ` + quoted(`{"version": 0}`) + "}\n{\"t\": " + quoted(answer) + "}", false, answer, nil},
		{"truncated in an element", answer[:strings.Index(answer, `"b"`)], true, `{"version": 1, "items": [{"id": "k1", "title": "a"}]}`, nil},
		{"truncated in a string", answer[:strings.Index(answer, `"b"`)+2], true, `{"version": 1, "items": [{"id": "k1", "title": "a"}]}`, nil},
		{"truncated after an array opens", `{"version": 1, "items": [`, true, `{"version": 1, "items": []}`, nil},
		{"complete answer preferred to salvage", answer + "\n{\"version\": 2, \"items\": [", true, answer, nil},
		{"truncated, not salvaged", "log " + answer[:40], false, "", ErrInvalidJSON},
		{"invalid answer, not an object nested in it", `{"version": 1, "items": [{"id": "k1", "title": "a}, {"id": "k2", "title": "b"}]}`, false, "", ErrInvalidJSON},
		{"no versioned object: last one", `{"a": 1} {"a": 2}`, false, `{"a": 2}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Extract([]byte(tt.out), tt.salvage)
			if tt.want == "" {
				if !errors.Is(err, tt.err) {
					t.Errorf("Extract() = %s, %v; want %v", got, err, tt.err)
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

func TestExtractInvalidAnswer(t *testing.T) {
	tests := []struct {
		name, out, want string
	}{
		// The raw tab and the invalid escape of line 3 are repaired: the
		// column must still be counted in what the agent printed.
		{
			"string left open",
			"log line\n{\"version\": 1,\n \"s\": \"a\tb\\d\",\n \"items\": [{\"id\": \"k1\", \"title\": \"a}, {\"id\": \"k2\"}]}\n",
			`line 4, column 41: invalid character 'i' after object key:value pair, at the end of "a\tb\\d\",\n \"items\": [{\"id\": \"k1\", \"title\": \"a}, {\"i"`,
		},
		{
			"truncated",
			`{"version": 1, "items": [{"id": "k1", "títle"`,
			`line 1, column 46: unexpected end of the output, at the end of "{\"version\": 1, \"items\": [{\"id\": \"k1\", \"títle\""`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Extract([]byte(tt.out), false)
			if !errors.Is(err, ErrInvalidJSON) || !strings.HasSuffix(err.Error(), tt.want) {
				t.Errorf("Extract() error = %v\nwant ErrInvalidJSON ending with %s", err, tt.want)
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
