package github

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
)

func TestParsePrivateKey(t *testing.T) {
	key := testKey(t)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecDER, _ := x509.MarshalPKCS8PrivateKey(ec)
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	for name, tc := range map[string]struct {
		data []byte
		ok   bool
	}{
		"PKCS #1, as GitHub gives it": {pkcs1, true},
		"PKCS #8":                     {pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), true},
		"an EC key":                   {pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}), false},
		"a key of 1024 bits":          {pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(small)}), false},
		"a certificate":               {pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")}), false},
		"a damaged key":               {[]byte(strings.Replace(string(pkcs1), "\n", "\nAAAA", 3)), false},
		"no PEM":                      {[]byte("secret-sentinel"), false},
	} {
		got, err := ParsePrivateKey(tc.data)
		if (err == nil) != tc.ok {
			t.Errorf("%s: ParsePrivateKey = %v", name, err)
		}
		if tc.ok && !got.Equal(key) {
			t.Errorf("%s: another key", name)
		}
		if err != nil && (strings.Contains(err.Error(), "sentinel") || strings.Contains(err.Error(), "AAAA")) {
			t.Errorf("%s: error %q holds the key", name, err)
		}
	}
}

func TestJWT(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	jwt, err := a.jwt()
	if err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest(http.MethodGet, "https://x", nil)
	r.Header.Set("Authorization", "Bearer "+jwt)
	if err := g.checkJWT(r); err != nil {
		t.Errorf("GitHub refuses the JWT: %v", err)
	}
	other := g.app(t, testKey(t), nil)
	jwt, _ = other.jwt()
	r.Header.Set("Authorization", "Bearer "+jwt)
	if err := g.checkJWT(r); err == nil {
		t.Error("GitHub accepts a JWT signed by another key")
	}
}

// contents is the permission of the scopes of the tests.
func contents() map[string]string { return map[string]string{"contents": "read"} }

func TestInstallationTokens(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	ctx := context.Background()
	api := scope{installation: 501, repositories: []int64{2002, 2001}, permissions: contents()}

	first, err := a.installationToken(ctx, api)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := a.installationToken(ctx, scope{installation: 501, repositories: []int64{2001, 2002}, permissions: contents()}); again != first {
		t.Error("the same scope in another order minted another token")
	}
	if len(g.recorded()) != 1 || g.recorded()[0].installation != 501 || !slices.Equal(g.recorded()[0].Repositories, []int64{2002, 2001}) || g.recorded()[0].Permissions["contents"] != "read" || len(g.recorded()[0].Permissions) != 1 {
		t.Fatalf("mints = %+v, want one of the exact scope", g.mints)
	}
	for _, s := range []scope{
		{installation: 501, repositories: []int64{2001}, permissions: contents()},
		{installation: 501, repositories: []int64{2001, 2002}, permissions: map[string]string{"contents": "read", "statuses": "write"}},
	} {
		if tok, _ := a.installationToken(ctx, s); tok == first {
			t.Errorf("scope %+v reused the token of another scope", s)
		}
	}
	if len(g.recorded()) != 3 {
		t.Errorf("%d mints, want one per scope", len(g.recorded()))
	}

	g.clock.Advance(time.Hour - tokenMargin - time.Second)
	if tok, _ := a.installationToken(ctx, api); tok != first {
		t.Error("a token with more than five minutes left was minted again")
	}
	g.clock.Advance(2 * time.Second)
	if tok, _ := a.installationToken(ctx, api); tok == first {
		t.Error("a token with less than five minutes left was reused")
	}
}

func TestInstallationTokensAreMintedOnce(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	release := make(chan struct{})
	g.change(func() {
		g.intercept = func(w http.ResponseWriter, r *http.Request) bool {
			if strings.HasSuffix(r.URL.Path, "/access_tokens") {
				<-release
			}
			return false
		}
	})
	var wg sync.WaitGroup
	tokens := make([]identity.Secret, 8)
	for i := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], _ = a.installationToken(context.Background(), scope{installation: 501, repositories: []int64{2001}, permissions: contents()})
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	for _, tok := range tokens {
		if tok == "" || tok != tokens[0] {
			t.Fatalf("tokens = %v, want one shared token", tokens)
		}
	}
	if len(g.recorded()) != 1 {
		t.Errorf("%d mints for one scope", len(g.recorded()))
	}
}

func TestARevokedInstallationIsNeverCached(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	ctx := context.Background()
	s := scope{installation: 501, repositories: []int64{2001}, permissions: contents()}
	if _, err := a.installationToken(ctx, s); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(){
		"suspended": func() { g.installations[501] = true },
		"removed":   func() { delete(g.installations, 501) },
	} {
		g.change(change)
		// The cached token is dropped, so the next call mints.
		a.forgetInstallation(501)
		for range 2 {
			_, err := a.installationToken(ctx, s)
			if !errors.Is(err, identity.ErrUnavailable) || !errors.Is(err, errInstallation) {
				t.Errorf("%s: installationToken = %v, want an unavailable installation", name, err)
			}
		}
		if len(a.tokens.tokens) != 0 {
			t.Errorf("%s: %d tokens cached", name, len(a.tokens.tokens))
		}
	}
	if n := len(g.recorded()); n != 5 {
		t.Errorf("%d mints, want each refusal asked again", n)
	}
	// Removing an installation drops every token of it.
	g.change(func() { g.installations[501] = false })
	if _, err := a.installationToken(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := a.installationToken(ctx, scope{installation: 501, repositories: []int64{2002}, permissions: contents()}); err != nil {
		t.Fatal(err)
	}
	a.forgetInstallation(501)
	if len(a.tokens.tokens) != 0 {
		t.Errorf("forgetInstallation left %d tokens", len(a.tokens.tokens))
	}
}

func TestARefusedInstallationTokenIsMintedAgainOnce(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	ctx := context.Background()
	s := scope{installation: 501, repositories: []int64{2001}, permissions: contents()}
	build := func(auth string) request { return a.apiGet("/installation/repositories", nil, auth) }
	if r, err := a.asInstallation(ctx, s, build); err != nil || r.status != http.StatusOK {
		t.Fatalf("asInstallation = %+v, %v", r, err)
	}
	// GitHub revokes the token before its expiry.
	g.change(func() { clear(g.installTokens) })
	if r, err := a.asInstallation(ctx, s, build); err != nil || r.status != http.StatusOK {
		t.Fatalf("asInstallation after a revoked token = %+v, %v", r, err)
	}
	if len(g.recorded()) != 2 {
		t.Errorf("%d mints, want one more after the refusal", len(g.recorded()))
	}
	// A new token refused as well is not minted a third time.
	g.change(func() {
		g.intercept = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/api/v3/installation/repositories" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
				return true
			}
			return false
		}
	})
	if _, err := a.asInstallation(ctx, s, build); !errors.Is(err, identity.ErrUnavailable) {
		t.Errorf("asInstallation with every token refused = %v", err)
	}
	if len(g.recorded()) != 3 {
		t.Errorf("%d mints, want one more and no other", len(g.recorded()))
	}
}

// TestALateRefusalKeepsTheNewToken: a refusal of an old token, arriving once
// its replacement is cached, never drops the replacement.
func TestALateRefusalKeepsTheNewToken(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	ctx := context.Background()
	s := scope{installation: 501, repositories: []int64{2001}, permissions: contents()}
	old, err := a.installationToken(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	a.forgetToken(s, old)
	fresh, err := a.installationToken(ctx, s)
	if err != nil || fresh == old {
		t.Fatalf("installationToken after a refusal = %v, %v", fresh, err)
	}
	a.forgetToken(s, old)
	if again, _ := a.installationToken(ctx, s); again != fresh || len(g.recorded()) != 2 {
		t.Errorf("a late refusal of the old token dropped the new one: %d mints", len(g.recorded()))
	}
}

// TestExpiredTokensLeaveTheCache: a token past its margin is not kept.
func TestExpiredTokensLeaveTheCache(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, key, nil)
	ctx := context.Background()
	first := scope{installation: 501, repositories: []int64{2001}, permissions: contents()}
	if _, err := a.installationToken(ctx, first); err != nil {
		t.Fatal(err)
	}
	g.clock.Advance(time.Hour - tokenMargin)
	if _, err := a.installationToken(ctx, scope{installation: 501, repositories: []int64{2002}, permissions: contents()}); err != nil {
		t.Fatal(err)
	}
	if n := len(a.tokens.tokens); n != 1 {
		t.Errorf("%d tokens cached, want only the one minted last", n)
	}
}

func TestAMintWithARefusedJWT(t *testing.T) {
	key := testKey(t)
	g := newGitHub(t, key)
	a := g.app(t, testKey(t), nil)
	_, err := a.installationToken(context.Background(), scope{installation: 501, repositories: []int64{2001}, permissions: contents()})
	if !errors.Is(err, identity.ErrUnavailable) || !strings.Contains(err.Error(), "JWT") || errors.Is(err, errInstallation) {
		t.Errorf("installationToken with another key = %v", err)
	}
}
