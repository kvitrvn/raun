package github

import (
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/core/identity"
)

func TestCheckWebURL(t *testing.T) {
	for _, tc := range []struct {
		url string
		ok  bool
	}{
		{"https://github.com", true},
		{"https://github.com/", true},
		{"https://ghe.example.com", true},
		{"https://ghe.example.com:8443", true},
		{"https://github.com:443", true},
		{"https://github.com:8443", false},
		{"http://ghe.example.com", false},
		{"https://user:pass@ghe.example.com", false},
		{"https://ghe.example.com/github", false},
		{"https://ghe.example.com/?x=1", false},
		{"https://ghe.example.com/#x", false},
		{"https://", false},
		{"ghe.example.com", false},
		{"", false},
	} {
		if err := CheckWebURL(tc.url); (err == nil) != tc.ok {
			t.Errorf("CheckWebURL(%q) = %v, want ok %v", tc.url, err, tc.ok)
		}
	}
}

func TestCheckIdentifier(t *testing.T) {
	for id, ok := range map[string]bool{"583231": true, "1": true, "0": false, "-1": false, "0583231": false, "alice": false, "": false, "1e3": false} {
		if err := CheckIdentifier(id); (err == nil) != ok {
			t.Errorf("CheckIdentifier(%q) = %v, want ok %v", id, err, ok)
		}
	}
}

func TestAPIOf(t *testing.T) {
	for web, want := range map[string]string{
		"https://github.com":          "https://api.github.com",
		"https://GitHub.com":          "https://api.github.com",
		"https://github.com:443":      "https://api.github.com",
		"https://ghe.example.com":     "https://ghe.example.com/api/v3",
		"https://ghe.example.com:444": "https://ghe.example.com:444/api/v3",
	} {
		if got := apiOf(web); got != want {
			t.Errorf("apiOf(%s) = %s, want %s", web, got, want)
		}
	}
}

func TestNew(t *testing.T) {
	key := testKey(t)
	valid := Options{AppID: 42, ClientID: "Iv1", ClientSecret: "s", PrivateKey: key, CallbackURL: "https://raun.example/login/callback"}
	a, err := New(valid)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name() != "github" || a.web != "https://github.com" || a.api != "https://api.github.com" {
		t.Errorf("App = %s %s %s", a.Name(), a.web, a.api)
	}
	ghes := valid
	ghes.WebURL = "https://ghe.example.com/"
	if a, err := New(ghes); err != nil || a.web != "https://ghe.example.com" || a.api != "https://ghe.example.com/api/v3" {
		t.Errorf("GHES App = %+v, %v", a, err)
	}
	for name, change := range map[string]func(o *Options){
		"web URL":       func(o *Options) { o.WebURL = "http://ghe.example.com" },
		"app ID":        func(o *Options) { o.AppID = 0 },
		"client ID":     func(o *Options) { o.ClientID = "" },
		"client secret": func(o *Options) { o.ClientSecret = "" },
		"private key":   func(o *Options) { o.PrivateKey = nil },
		"callback URL":  func(o *Options) { o.CallbackURL = "/login/callback" },
	} {
		o := valid
		change(&o)
		if _, err := New(o); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("New with a bad %s = %v", name, err)
		}
	}
	if _, err := New(Options{ClientSecret: identity.Secret("secret-sentinel")}); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
		t.Errorf("New = %v", err)
	}
}
