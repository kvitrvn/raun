package identity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretNeverPrints(t *testing.T) {
	const value = "gho_secret-sentinel"
	tokens := Tokens{Access: Secret(value), Refresh: Secret(value)}
	var outputs []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		outputs = append(outputs, fmt.Sprintf(verb, Secret(value)), fmt.Sprintf(verb, tokens))
	}
	b, err := json.Marshal(tokens)
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, string(b))
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("tokens", "access", Secret(value), "tokens", tokens)
	slog.New(slog.NewTextHandler(&logs, nil)).Info("tokens", "access", Secret(value))
	outputs = append(outputs, logs.String())
	for _, out := range outputs {
		if strings.Contains(out, value) {
			t.Errorf("a secret printed: %s", out)
		}
	}
	if Secret(value).Reveal() != value {
		t.Error("Reveal does not give the value")
	}
}

func TestChallengeFollowsRFC7636(t *testing.T) {
	// The example of RFC 7636, appendix B.
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("Challenge = %s", got)
	}
}

func TestNewVerifier(t *testing.T) {
	v, err := NewVerifier(bytes.NewReader(bytes.Repeat([]byte{1}, 32)))
	if err != nil || len(v) != 43 || strings.ContainsAny(v, "+/=") {
		t.Errorf("NewVerifier = %q, %v", v, err)
	}
	if _, err := NewVerifier(bytes.NewReader(make([]byte, 10))); err == nil {
		t.Error("NewVerifier succeeded with 10 random bytes")
	}
}

func TestKey(t *testing.T) {
	if got := (Identity{Forge: "github", ID: "583231", Login: "octocat"}).Key(); got != "github:583231" {
		t.Errorf("Key = %s", got)
	}
}
