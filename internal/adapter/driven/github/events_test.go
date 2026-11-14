package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/core/repository"
	"github.com/kvitrvn/raun/internal/golden"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func hookHeaders(event, delivery, signature string) func(string) string {
	h := map[string]string{headerEvent: event, headerDelivery: delivery, headerSignature: signature}
	return func(name string) string { return h[name] }
}

func eventsApp(t *testing.T) *App {
	t.Helper()
	a, err := New(Options{AppID: 42, ClientID: "Iv1", ClientSecret: "s", PrivateKey: testKey(t), WebhookSecret: webhookSecret, CallbackURL: "https://raun.example/login/callback"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestNormalizedEvents parses the recorded payloads of GitHub into the
// normalized deliveries of their golden files.
func TestNormalizedEvents(t *testing.T) {
	a := eventsApp(t)
	files, err := filepath.Glob("testdata/hooks/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no payload: %v", err)
	}
	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			event, _, _ := strings.Cut(name, "-")
			d, err := a.Parse(hookHeaders(event, "72d3162e-cc78-11e3-81ab-4c9367dc0958", sign(webhookSecret, body)), body)
			if err != nil {
				t.Fatal(err)
			}
			// The golden file holds the fork marker too, which epic 7
			// reads at its policy point.
			out := struct {
				Delivery repository.Delivery
				Fork     *bool `json:",omitempty"`
			}{Delivery: d}
			if d.PullRequest != nil {
				fork := d.PullRequest.Fork()
				out.Fork = &fork
			}
			got, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Equal(t, "testdata/hooks/"+name+".golden", append(got, '\n'))
		})
	}
}

// TestSignatureFirst: every refused signature is the same error, and no
// refused delivery is parsed.
func TestSignatureFirst(t *testing.T) {
	a := eventsApp(t)
	body := []byte(`{"ref":"refs/heads/main"`) // not even JSON: never read
	good := sign(webhookSecret, body)
	for name, signature := range map[string]string{
		"none":             "",
		"no prefix":        strings.TrimPrefix(good, "sha256="),
		"sha1":             "sha1=" + strings.TrimPrefix(good, "sha256="),
		"not hexadecimal":  "sha256=zz" + good[9:],
		"truncated":        good[:len(good)-2],
		"another secret":   sign("another-secret", body),
		"uppercase prefix": "SHA256=" + strings.TrimPrefix(good, "sha256="),
		"of another body":  sign(webhookSecret, []byte("{}")),
		"with a space":     " " + good,
	} {
		_, err := a.Parse(hookHeaders("push", "d1", signature), body)
		if err != repository.ErrSignature {
			t.Errorf("%s: %v, want exactly ErrSignature", name, err)
		}
	}
	// A signed body that is not one of GitHub is malformed, not refused.
	if _, err := a.Parse(hookHeaders("push", "d1", good), body); !errors.Is(err, repository.ErrMalformed) {
		t.Errorf("a signed malformed body = %v", err)
	}
	// Without a secret, nothing is signed.
	none := *a
	none.webhookSecret = ""
	if _, err := none.Parse(hookHeaders("push", "d1", sign("", body)), body); err != repository.ErrSignature {
		t.Errorf("without a secret = %v", err)
	}
}

func TestMalformedDeliveries(t *testing.T) {
	a := eventsApp(t)
	push, err := os.ReadFile("testdata/hooks/push-branch.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, event, delivery, body string
	}{
		{"no delivery", "push", "", string(push)},
		{"a delivery too long", "push", strings.Repeat("a", 129), string(push)},
		{"a delivery with a slash", "push", "a/b", string(push)},
		{"no event", "", "d1", string(push)},
		{"an event in capitals", "Push", "d1", string(push)},
		{"an array", "push", "d1", `[]`},
		{"two values", "push", "d1", `{} {}`},
		{"a push without commits", "push", "d1", `{"ref":"refs/heads/main","repository":{"id":2001}}`},
		{"a push without its repository", "push", "d1", `{"ref":"refs/heads/main","before":"` + strings.Repeat("a", 40) + `","after":"` + strings.Repeat("b", 40) + `"}`},
		{"a repository without its identifier", "issues", "d1", `{"repository":{"full_name":"acme/api"}}`},
		{"a pull request without its base", "pull_request", "d1", `{"action":"opened","number":1,"repository":{"id":2001},"pull_request":{"head":{"ref":"a","sha":"` + strings.Repeat("a", 40) + `"}}}`},
		{"an identifier as text", "push", "d1", `{"repository":{"id":"2001"}}`},
	} {
		body := []byte(tc.body)
		if _, err := a.Parse(hookHeaders(tc.event, tc.delivery, sign(webhookSecret, body)), body); !errors.Is(err, repository.ErrMalformed) {
			t.Errorf("%s: %v, want ErrMalformed", tc.name, err)
		}
	}
}
