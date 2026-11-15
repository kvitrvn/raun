package runnerapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParsePath(t *testing.T) {
	for _, tt := range []struct {
		path    string
		version int
		rest    string
		ok      bool
	}{
		{"/api/runner/v1/claim", 1, "claim", true},
		{"/api/runner/v1/runs/abc/log", 1, "runs/abc/log", true},
		{"/api/runner/v2/claim", 2, "claim", true},
		{"/api/runner/v1", 1, "", true},
		{"/api/runner/", 0, "", false},
		{"/api/runner/claim", 0, "", false},
		{"/api/runner/v/claim", 0, "claim", false},
		{"/api/runner/v01/claim", 0, "claim", false},
		{"/api/runner/v-1/claim", 0, "claim", false},
		{"/api/runner/v99999/claim", 0, "claim", false},
		{"/api/other/v1/claim", 0, "", false},
	} {
		v, rest, ok := ParsePath(tt.path)
		if v != tt.version || rest != tt.rest || ok != tt.ok {
			t.Errorf("ParsePath(%q) = %d, %q, %v; want %d, %q, %v", tt.path, v, rest, ok, tt.version, tt.rest, tt.ok)
		}
	}
}

func TestVersions(t *testing.T) {
	if !Speaks(Version) || Speaks(Version+1) || Speaks(0) || SpokenText() != "1" {
		t.Errorf("Speaks or SpokenText: %v %q", Spoken(), SpokenText())
	}
	s := Spoken()
	s[0] = 9
	if !Speaks(1) {
		t.Error("Spoken gave its own table")
	}
	for _, tt := range []struct {
		in string
		v  int
		ok bool
	}{{"1", 1, true}, {"2", 2, true}, {"", 0, false}, {"01", 0, false}, {"v1", 0, false}, {"1 ", 0, false}, {"99999", 0, false}} {
		if v, ok := ParseVersion(tt.in); v != tt.v || ok != tt.ok {
			t.Errorf("ParseVersion(%q) = %d, %v", tt.in, v, ok)
		}
	}
	if Release("raun-runner/1.2.0") != "1.2.0" || Release("curl/8") != "" || Release("") != "" {
		t.Error("Release")
	}
}

func TestDecodeIsStrict(t *testing.T) {
	var c Chunk
	if err := Decode([]byte(` {"seq": 2, "text": "aé"} `), &c); err != nil || *c.Seq != 2 || *c.Text != "aé" {
		t.Fatalf("Decode = %+v, %v", c, err)
	}
	for _, body := range []string{
		``, `null`, `[]`, `"x"`, `{"seq": 1, "text": "x", "other": 1}`, `{"seq": 1}{}`, `{"seq": 1} x`,
		`{"seq": "1"}`, `{`,
	} {
		var c Chunk
		if err := Decode([]byte(body), &c); !errors.Is(err, ErrMalformed) {
			t.Errorf("Decode(%q) = %v, want ErrMalformed", body, err)
		}
	}
	var e Empty
	if err := Decode([]byte(`{}`), &e); err != nil {
		t.Errorf("Decode({}) = %v", err)
	}
	if err := Decode([]byte(`{"x": 1}`), &e); !errors.Is(err, ErrMalformed) {
		t.Errorf("Decode of a field in an empty body = %v", err)
	}
}

// TestMessagesAreTheDocumentedOnes: the JSON of the messages is the one
// docs/runner.md shows.
func TestMessagesAreTheDocumentedOnes(t *testing.T) {
	at := time.Date(2026, 11, 14, 15, 1, 0, 0, time.UTC)
	for _, tt := range []struct {
		v    any
		want string
	}{
		{Renewal{Expires: at}, `{"expires":"2026-11-14T15:01:00Z","cancel":false}`},
		{Renewal{Cancel: true}, `{"cancel":true}`},
		{Error{Error: "x"}, `{"error":"x"}`},
		{Result{Outcome: "succeeded", Summary: "s"}, `{"outcome":"succeeded","summary":"s"}`},
		{Assignment{Run: "r", Lease: Lease{Token: "l", Expires: at}, Deadline: at,
			Job:   Job{Kind: "noop", Repository: Repository{Forge: "github", ID: "2001", FullName: "acme/api"}, Commit: "c", Ref: "refs/heads/main", Params: json.RawMessage(`{}`)},
			Clone: Clone{URL: "https://github.com/acme/api.git", Username: "x-access-token", Password: "p", Expires: at}},
			`{"run":"r","lease":{"token":"l","expires":"2026-11-14T15:01:00Z"},"deadline":"2026-11-14T15:01:00Z",` +
				`"job":{"kind":"noop","repository":{"forge":"github","id":"2001","full_name":"acme/api"},"commit":"c","ref":"refs/heads/main","params":{}},` +
				`"clone":{"url":"https://github.com/acme/api.git","username":"x-access-token","password":"p","expires":"2026-11-14T15:01:00Z"}}`},
	} {
		b, err := json.Marshal(tt.v)
		if err != nil || string(b) != tt.want {
			t.Errorf("Marshal(%T) = %s, %v\nwant %s", tt.v, b, err, tt.want)
		}
	}
	n := 3
	if b, _ := json.Marshal(Error{Error: "x", Expected: &n}); !strings.Contains(string(b), `"expected":3`) {
		t.Errorf("Error with expected = %s", b)
	}
}
