package main

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/core/identity"
)

func TestLoadConfigIdentities(t *testing.T) {
	keyFile, _ := writeAppKey(t, t.TempDir())
	c, err := loadConfig(append(app(keyFile),
		"RAUN_SIGNUP_USERS=github:583231, github:1",
		"RAUN_SIGNUP_ORGS=github:9919",
		"RAUN_ADMINS=github:42",
		"RAUN_SECRET_KEY_PREVIOUS="+secretKey(2),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.signup.Users) != 2 || c.signup.Users[0] != (identity.Entry{Forge: "github", ID: "583231"}) ||
		len(c.signup.Orgs) != 1 || len(c.signup.Admins) != 1 {
		t.Errorf("signup = %+v", c.signup)
	}
	if !bytes.Equal(c.secretKey, bytes.Repeat([]byte{1}, 32)) || !bytes.Equal(c.previousKey, bytes.Repeat([]byte{2}, 32)) {
		t.Error("the secret keys are not read")
	}
	if c, err := loadConfig(nil); err != nil || c.secretKey != nil || len(c.signup.Users) != 0 {
		t.Errorf("defaults = %+v, %v", c.signup, err)
	}
}

func TestLoadConfigRefusesIdentities(t *testing.T) {
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	short := base64.StdEncoding.EncodeToString(make([]byte, 31))
	without := func(name string) []string {
		var out []string
		for _, kv := range app(keyFile) {
			if !strings.HasPrefix(kv, name+"=") {
				out = append(out, kv)
			}
		}
		return out
	}
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"an entry without a forge", []string{"RAUN_SIGNUP_USERS=583231"}, "RAUN_SIGNUP_USERS: \"583231\" is not of the form forge:identifier"},
		{"a login for an organization", []string{"RAUN_SIGNUP_ORGS=acme"}, "RAUN_SIGNUP_ORGS:"},
		{"an administrator without a forge", []string{"RAUN_ADMINS=github:42"}, "RAUN_ADMINS: github:42 names the forge \"github\", which is not configured"},
		{"an entry of another forge", append(app(keyFile), "RAUN_SIGNUP_USERS=gitlab:7"), "RAUN_SIGNUP_USERS: gitlab:7 names the forge \"gitlab\""},
		{"a GitHub login for a user", append(app(keyFile), "RAUN_SIGNUP_USERS=github:alice"), "RAUN_SIGNUP_USERS: github:alice: \"alice\" is not the stable identifier"},
		{"a GitHub login for an organization", append(app(keyFile), "RAUN_SIGNUP_ORGS=github:acme"), "RAUN_SIGNUP_ORGS: github:acme:"},
		{"a GitHub login for an administrator", append(app(keyFile), "RAUN_ADMINS=github:octocat"), "RAUN_ADMINS: github:octocat:"},
		{"a forge without a secret key", without("RAUN_SECRET_KEY"), "RAUN_SECRET_KEY: required with a forge"},
		{"a key of 31 bytes", []string{"RAUN_SECRET_KEY=" + short}, "RAUN_SECRET_KEY: not 32 bytes in base64"},
		{"a key that is not base64", []string{"RAUN_SECRET_KEY=key-sentinel!"}, "RAUN_SECRET_KEY: not 32 bytes in base64"},
		{"a key in URL base64", []string{"RAUN_SECRET_KEY=" + base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32))}, "RAUN_SECRET_KEY:"},
		{"a previous key alone", []string{"RAUN_SECRET_KEY_PREVIOUS=" + secretKey(2)}, "RAUN_SECRET_KEY_PREVIOUS: set without RAUN_SECRET_KEY"},
		{"the same key twice", append(app(keyFile), "RAUN_SECRET_KEY_PREVIOUS="+secretKey(1)), "RAUN_SECRET_KEY_PREVIOUS: the same key as RAUN_SECRET_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadConfig(tc.env)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "sentinel") || strings.Contains(err.Error(), secretKey(1)) {
				t.Errorf("error %q holds a secret", err)
			}
		})
	}
}

func TestSecretKeysAreNeverShown(t *testing.T) {
	keyFile, _ := writeAppKey(t, t.TempDir())
	c, err := loadConfig(append(app(keyFile), "RAUN_SECRET_KEY_PREVIOUS="+secretKey(2)))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range c.logAttrs() {
		if v := a.Value.String(); strings.Contains(v, secretKey(1)) || strings.Contains(v, secretKey(2)) {
			t.Errorf("%s shows a secret key", a.Key)
		}
	}
}
