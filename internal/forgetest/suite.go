package forgetest

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/identity"
	"github.com/kvitrvn/raun/internal/core/repository"
)

// FilesHead is the head of main in acme/api in the scenario of the suite.
const FilesHead = "cccccccccccccccccccccccccccccccccccccccc"

// Fixture is a forge set up with the scenario of the contract suite:
//
//   - a person, Alice, who belongs to the organization Org and not to OtherOrg;
//   - acme/api, where she decides, and acme/web, where she views;
//   - Unreachable: acme/secret, which she cannot see, and other/lib, on
//     which she has write access on the forge but which Raun cannot reach.
//
// An adapter builds it from recorded responses; New builds it from the fake.
type Fixture struct {
	Forge    identity.Forge
	Alice    string
	Org      string
	OtherOrg string
	// Unreachable are the two repositories Alice must not reach.
	Unreachable []identity.Repository
	// Reached are the identifiers of the repositories the App reaches,
	// sorted: acme/api, acme/web and acme/secret.
	Reached []string
	// Head is the commit at the head of main in acme/api, where
	// .raun.yaml holds "version: 1\n", big.txt 100 bytes and empty.txt
	// nothing. The other repositories hold no file.
	Head string
	// Push is the forge delivering, signed, a push to the main branch of
	// acme/api from Alice: the headers of the request and its body; nil
	// for a forge without the events capability.
	Push func(t *testing.T) (map[string]string, []byte)
	// Approve is Alice approving Raun at url: the code and the state the
	// forge sends back.
	Approve func(t *testing.T, url string) (code, state string)
	// ExpireCodes makes the forge refuse the authorization codes it gave,
	// as time passes, while the tokens it gave stay valid.
	ExpireCodes func(t *testing.T)
	// ExpireAccess makes the forge refuse the access tokens it gave, as
	// time passes, while their refresh tokens stay valid. Time passes for
	// the adapter too: the expiries it computes afterwards are later.
	ExpireAccess func(t *testing.T)
	// ExpireRefresh makes the forge refuse the refresh tokens it gave, as
	// time passes.
	ExpireRefresh func(t *testing.T)
	// Revoke makes the forge refuse Alice's authorization for good.
	Revoke func(t *testing.T)
	// SetDown makes the forge unavailable, or available again.
	SetDown func(t *testing.T, down bool)
	// RenameAlice gives Alice a new login on the forge.
	RenameAlice func(t *testing.T, login string)
}

// Scenario returns the fake forge called name set up with the scenario of
// the suite, and its fixture. bare drops the optional capabilities.
func Scenario(name string, bare bool) (*Fake, Fixture) {
	start := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	f := New(name, func() time.Time { return start })
	f.AddPerson(Person{ID: "1001", Login: "alice", Name: "Alice Martin"})
	f.AddPerson(Person{ID: "1002", Login: "bob", Name: "Bob Durand"})
	repos := []struct {
		r         identity.Repository
		installed bool
		alice     identity.Permission
	}{
		{identity.Repository{ID: "2001", FullName: "acme/api", DefaultBranch: "main", Private: true}, true, identity.PermissionDecide},
		{identity.Repository{ID: "2002", FullName: "acme/web", DefaultBranch: "main"}, true, identity.PermissionView},
		{identity.Repository{ID: "2003", FullName: "acme/secret", DefaultBranch: "main", Private: true}, true, ""},
		{identity.Repository{ID: "2004", FullName: "other/lib", DefaultBranch: "trunk"}, false, identity.PermissionDecide},
	}
	var unreachable []identity.Repository
	for _, r := range repos {
		f.AddRepository(r.r, r.installed)
		if r.alice != "" {
			f.Grant("1001", r.r.ID, r.alice)
		}
		f.Grant("1002", r.r.ID, identity.PermissionDecide)
		if r.r.FullName == "acme/secret" || r.r.FullName == "other/lib" {
			rr := r.r
			rr.Forge = name
			unreachable = append(unreachable, rr)
		}
	}
	f.AddMember("3001", "1001")
	f.AddMember("3002", "1002")

	f.SetHead("2001", "main", FilesHead)
	f.SetHead("2002", "main", strings.Repeat("e", 40))
	f.SetFile("2001", FilesHead, ".raun.yaml", "version: 1\n")
	f.SetFile("2001", FilesHead, "big.txt", strings.Repeat("x", 100))
	f.SetFile("2001", FilesHead, "empty.txt", "")

	var forge identity.Forge = f
	if bare {
		forge = f.Bare()
	}
	return f, Fixture{
		Forge:       forge,
		Alice:       "1001",
		Org:         "3001",
		OtherOrg:    "3002",
		Unreachable: unreachable,
		Reached:     []string{"2001", "2002", "2003"},
		Head:        FilesHead,
		Push: func(*testing.T) (map[string]string, []byte) {
			return f.Deliver("delivery-1", repository.Delivery{
				Event: "push", Kind: repository.KindPush, Repository: "2001",
				Push: &repository.Push{Ref: "refs/heads/main", Before: strings.Repeat("a", 40), After: strings.Repeat("b", 40), Head: strings.Repeat("b", 40), Sender: repository.Sender{ID: "1001", Login: "alice"}},
			})
		},
		Approve: func(t *testing.T, url string) (string, string) {
			t.Helper()
			code, state, err := f.Approve("1001", url)
			if err != nil {
				t.Fatal(err)
			}
			return code, state
		},
		ExpireCodes:   func(*testing.T) { f.Advance(CodeLifetime) },
		ExpireAccess:  func(*testing.T) { f.Advance(AccessLifetime + time.Second) },
		ExpireRefresh: func(*testing.T) { f.Advance(RefreshLifetime + time.Second) },
		Revoke:        func(*testing.T) { f.Revoke("1001") },
		SetDown:       func(_ *testing.T, down bool) { f.SetDown(down) },
		RenameAlice: func(t *testing.T, login string) {
			t.Helper()
			if !f.Rename("1001", login) {
				t.Fatal("Alice is unknown")
			}
		},
	}
}

// noSecret fails the test if err holds one of the secret values: an error is
// logged, a token, a code or a verifier never is.
func noSecret(t *testing.T, err error, values ...string) {
	t.Helper()
	if err == nil {
		return
	}
	for _, v := range values {
		if v != "" && strings.Contains(err.Error(), v) {
			t.Errorf("error %q holds a secret", err)
		}
	}
}

// noToken fails the test if err holds the value of one of the tokens.
func noToken(t *testing.T, err error, tokens ...identity.Tokens) {
	t.Helper()
	var values []string
	for _, tk := range tokens {
		values = append(values, tk.Access.Reveal(), tk.Refresh.Reveal())
	}
	noSecret(t, err, values...)
}

// Suite runs the contract of the forge port on the fixtures newFixture
// builds, one per case.
func Suite(t *testing.T, newFixture func(t *testing.T) Fixture) {
	ctx := context.Background()

	// authorize returns a verifier and the authorization URL for state,
	// after checking that the URL carries the state and the S256 challenge
	// of the verifier.
	authorize := func(t *testing.T, fx Fixture, state string) (string, string) {
		t.Helper()
		verifier, err := identity.NewVerifier(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		raw := fx.Forge.AuthorizeURL(state, identity.Challenge(verifier))
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("AuthorizeURL gave %q: %v", raw, err)
		}
		q := u.Query()
		if q.Get("state") != state || q.Get("code_challenge") != identity.Challenge(verifier) || q.Get("code_challenge_method") != "S256" {
			t.Fatalf("AuthorizeURL gave %q, want the state and the S256 challenge of the verifier", raw)
		}
		return verifier, raw
	}

	login := func(t *testing.T, fx Fixture) (identity.Identity, identity.Tokens) {
		t.Helper()
		state := "state-of-the-suite"
		verifier, raw := authorize(t, fx, state)
		code, back := fx.Approve(t, raw)
		if back != state {
			t.Fatalf("the forge sent back state %q, want %q", back, state)
		}
		id, tokens, err := fx.Forge.Exchange(ctx, code, verifier)
		if err != nil {
			t.Fatalf("Exchange: %v", err)
		}
		return id, tokens
	}

	// decided is the repository of the fixture where Alice decides, as the
	// forge lists it.
	decided := func(t *testing.T, fx Fixture, tokens identity.Tokens) identity.Repository {
		t.Helper()
		repos, err := fx.Forge.Repositories(ctx, tokens)
		if err != nil {
			t.Fatalf("Repositories: %v", err)
		}
		for _, r := range repos {
			if r.FullName == "acme/api" {
				return r
			}
		}
		t.Fatalf("Repositories = %v, want acme/api among them", repos)
		return identity.Repository{}
	}

	// refused checks that every call that uses the access token of tokens
	// fails with want, and that no error holds a token.
	refused := func(t *testing.T, fx Fixture, tokens identity.Tokens, repo identity.Repository, want error) {
		t.Helper()
		_, err := fx.Forge.Repositories(ctx, tokens)
		if !errors.Is(err, want) {
			t.Errorf("Repositories = %v, want %v", err, want)
		}
		noToken(t, err, tokens)
		p, err := fx.Forge.Permission(ctx, tokens, repo)
		if !errors.Is(err, want) {
			t.Errorf("Permission(%s) = %s, %v; want %v", repo.FullName, p, err, want)
		}
		noToken(t, err, tokens)
		if m, ok := identity.MembershipsOf(fx.Forge); ok {
			in, err := m.IsMember(ctx, tokens, fx.Org)
			if !errors.Is(err, want) {
				t.Errorf("IsMember(Org) = %v, %v; want %v", in, err, want)
			}
			noToken(t, err, tokens)
		}
	}

	// expiries checks the expiries of tokens given by call. The suite does
	// not know the clock of the forge: it checks the expiries against each
	// other, and against time passing on Refresh.
	expiries := func(t *testing.T, call string, tokens identity.Tokens) {
		t.Helper()
		if tokens.AccessExpiry.IsZero() || !tokens.AccessExpiry.Before(tokens.RefreshExpiry) {
			t.Errorf("%s gave expiries %v and %v, want an access expiry before the refresh expiry", call, tokens.AccessExpiry, tokens.RefreshExpiry)
		}
	}

	t.Run("login", func(t *testing.T) {
		fx := newFixture(t)
		id, tokens := login(t, fx)
		if id.Forge != fx.Forge.Name() || id.ID != fx.Alice || id.Login == "" {
			t.Errorf("identity = %+v", id)
		}
		if tokens.Access.Reveal() == "" || tokens.Refresh.Reveal() == "" {
			t.Error("Exchange gave an empty token")
		}
		expiries(t, "Exchange", tokens)
	})

	t.Run("a code serves once and needs its verifier", func(t *testing.T) {
		fx := newFixture(t)
		verifier, raw := authorize(t, fx, "s")
		other, _ := identity.NewVerifier(rand.Reader)
		code, _ := fx.Approve(t, raw)
		_, _, err := fx.Forge.Exchange(ctx, code, other)
		if !errors.Is(err, identity.ErrInvalidCode) {
			t.Errorf("Exchange with another verifier = %v, want ErrInvalidCode", err)
		}
		noSecret(t, err, code, other, verifier)
		code, _ = fx.Approve(t, raw)
		if _, _, err := fx.Forge.Exchange(ctx, code, verifier); err != nil {
			t.Fatalf("Exchange: %v", err)
		}
		_, _, err = fx.Forge.Exchange(ctx, code, verifier)
		if !errors.Is(err, identity.ErrInvalidCode) {
			t.Errorf("second Exchange of a code = %v, want ErrInvalidCode", err)
		}
		noSecret(t, err, code, verifier)
		_, _, err = fx.Forge.Exchange(ctx, "not-a-code", verifier)
		if !errors.Is(err, identity.ErrInvalidCode) {
			t.Errorf("Exchange of an unknown code = %v, want ErrInvalidCode", err)
		}
		noSecret(t, err, verifier)
	})

	t.Run("a code expires", func(t *testing.T) {
		fx := newFixture(t)
		verifier, raw := authorize(t, fx, "s")
		code, _ := fx.Approve(t, raw)
		fx.ExpireCodes(t)
		_, _, err := fx.Forge.Exchange(ctx, code, verifier)
		if !errors.Is(err, identity.ErrInvalidCode) {
			t.Errorf("Exchange of an expired code = %v, want ErrInvalidCode", err)
		}
		noSecret(t, err, code, verifier)
	})

	t.Run("repositories", func(t *testing.T) {
		fx := newFixture(t)
		_, tokens := login(t, fx)
		repos, err := fx.Forge.Repositories(ctx, tokens)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, r := range repos {
			names = append(names, r.FullName)
			if r.Forge != fx.Forge.Name() || r.ID == "" || r.DefaultBranch == "" {
				t.Errorf("repository %+v", r)
			}
		}
		if !slices.Equal(names, []string{"acme/api", "acme/web"}) {
			t.Errorf("repositories = %v, want acme/api and acme/web in order", names)
		}
	})

	t.Run("permissions", func(t *testing.T) {
		fx := newFixture(t)
		_, tokens := login(t, fx)
		repos, err := fx.Forge.Repositories(ctx, tokens)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]identity.Permission{"acme/api": identity.PermissionDecide, "acme/web": identity.PermissionView}
		for _, r := range append(repos, fx.Unreachable...) {
			got, err := fx.Forge.Permission(ctx, tokens, r)
			if err != nil {
				t.Fatalf("Permission(%s): %v", r.FullName, err)
			}
			w, ok := want[r.FullName]
			if !ok {
				w = identity.PermissionNone
			}
			if got != w {
				t.Errorf("Permission(%s) = %s, want %s", r.FullName, got, w)
			}
		}
	})

	t.Run("an expired access token is refused, then refreshed", func(t *testing.T) {
		fx := newFixture(t)
		_, tokens := login(t, fx)
		repo := decided(t, fx, tokens)
		fx.ExpireAccess(t)
		refused(t, fx, tokens, repo, identity.ErrUnauthorized)
		fresh, err := fx.Forge.Refresh(ctx, tokens)
		if err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		expiries(t, "Refresh", fresh)
		if fresh.Access.Reveal() == "" || fresh.Access.Reveal() == tokens.Access.Reveal() {
			t.Error("Refresh gave no new access token")
		}
		if !fresh.AccessExpiry.After(tokens.AccessExpiry) {
			t.Errorf("Refresh after the expiry gave an access expiry of %v, want after %v", fresh.AccessExpiry, tokens.AccessExpiry)
		}
		if _, err := fx.Forge.Repositories(ctx, fresh); err != nil {
			t.Errorf("Repositories after Refresh: %v", err)
		}
		if p, err := fx.Forge.Permission(ctx, fresh, repo); err != nil || p != identity.PermissionDecide {
			t.Errorf("Permission(%s) after Refresh = %s, %v; want decide", repo.FullName, p, err)
		}
	})

	// GitHub and GitLab refuse a refresh token once used, Gitea only when
	// told to: the old one may still work, and the caller never relies on it.
	t.Run("a refresh rotates the refresh token", func(t *testing.T) {
		fx := newFixture(t)
		_, tokens := login(t, fx)
		fresh, err := fx.Forge.Refresh(ctx, tokens)
		if err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		expiries(t, "Refresh", fresh)
		if fresh.Refresh.Reveal() == "" || fresh.Refresh.Reveal() == tokens.Refresh.Reveal() {
			t.Error("Refresh gave no new refresh token")
		}
		if _, err := fx.Forge.Refresh(ctx, fresh); err != nil {
			t.Errorf("Refresh of the new refresh token: %v", err)
		}
		_, err = fx.Forge.Refresh(ctx, tokens)
		if err != nil && !errors.Is(err, identity.ErrRevoked) {
			t.Errorf("Refresh of a used refresh token = %v, want ErrRevoked if the forge refuses it", err)
		}
		noToken(t, err, tokens, fresh)
	})

	t.Run("an expired refresh token is refused for good", func(t *testing.T) {
		fx := newFixture(t)
		_, tokens := login(t, fx)
		fx.ExpireRefresh(t)
		_, err := fx.Forge.Refresh(ctx, tokens)
		if !errors.Is(err, identity.ErrRevoked) {
			t.Errorf("Refresh of an expired refresh token = %v, want ErrRevoked", err)
		}
		noToken(t, err, tokens)
	})

	t.Run("a revoked authorization is refused for good", func(t *testing.T) {
		fx := newFixture(t)
		_, tokens := login(t, fx)
		repo := decided(t, fx, tokens)
		fx.Revoke(t)
		_, err := fx.Forge.Refresh(ctx, tokens)
		if !errors.Is(err, identity.ErrRevoked) {
			t.Errorf("Refresh after revocation = %v, want ErrRevoked", err)
		}
		noToken(t, err, tokens)
		refused(t, fx, tokens, repo, identity.ErrUnauthorized)
	})

	t.Run("an unavailable forge is a transient error", func(t *testing.T) {
		fx := newFixture(t)
		_, tokens := login(t, fx)
		repo := decided(t, fx, tokens)
		verifier, raw := authorize(t, fx, "s")
		code, _ := fx.Approve(t, raw)
		fx.SetDown(t, true)
		_, _, err := fx.Forge.Exchange(ctx, code, verifier)
		if !errors.Is(err, identity.ErrUnavailable) {
			t.Errorf("Exchange while down = %v, want ErrUnavailable", err)
		}
		noSecret(t, err, code, verifier)
		refused(t, fx, tokens, repo, identity.ErrUnavailable)
		_, err = fx.Forge.Refresh(ctx, tokens)
		if !errors.Is(err, identity.ErrUnavailable) {
			t.Errorf("Refresh while down = %v, want ErrUnavailable", err)
		}
		noToken(t, err, tokens)
		fx.SetDown(t, false)
		// A transient failure takes nothing away: the tokens still work.
		if _, err := fx.Forge.Repositories(ctx, tokens); err != nil {
			t.Errorf("Repositories once back: %v", err)
		}
		if p, err := fx.Forge.Permission(ctx, tokens, repo); err != nil || p != identity.PermissionDecide {
			t.Errorf("Permission(%s) once back = %s, %v; want decide", repo.FullName, p, err)
		}
		fresh, err := fx.Forge.Refresh(ctx, tokens)
		if err != nil {
			t.Fatalf("Refresh once back: %v", err)
		}
		expiries(t, "Refresh", fresh)
		if _, err := fx.Forge.Repositories(ctx, fresh); err != nil {
			t.Errorf("Repositories after the Refresh once back: %v", err)
		}
	})

	t.Run("a renamed login keeps its identifier", func(t *testing.T) {
		fx := newFixture(t)
		before, _ := login(t, fx)
		fx.RenameAlice(t, "alice-renamed")
		after, _ := login(t, fx)
		if after.ID != before.ID || after.Login != "alice-renamed" || after.Key() != before.Key() {
			t.Errorf("after a rename: %+v, before %+v", after, before)
		}
	})

	t.Run("reach, when the forge has it", func(t *testing.T) {
		fx := newFixture(t)
		r, ok := fx.Forge.(repository.Reach)
		if !ok {
			t.Skip("the forge cannot tell which repositories its App reaches")
		}
		got, err := r.Reached(ctx)
		slices.Sort(got)
		if err != nil || !slices.Equal(got, fx.Reached) {
			t.Errorf("Reached = %v, %v; want %v", got, err, fx.Reached)
		}
		fx.SetDown(t, true)
		if got, err := r.Reached(ctx); got != nil || !errors.Is(err, identity.ErrUnavailable) {
			t.Errorf("Reached of a forge down = %v, %v; want nothing and ErrUnavailable", got, err)
		}
	})

	t.Run("files, when the forge has them", func(t *testing.T) {
		fx := newFixture(t)
		f, ok := fx.Forge.(repository.Files)
		if !ok {
			t.Skip("the forge cannot read files")
		}
		if head, err := f.Head(ctx, "2001", "main"); err != nil || head != fx.Head {
			t.Fatalf("Head(main) = %q, %v; want %q", head, err, fx.Head)
		}
		if _, err := f.Head(ctx, "2001", "no-such-branch"); !errors.Is(err, repository.ErrNoFile) {
			t.Errorf("Head of a missing branch = %v, want ErrNoFile", err)
		}
		read := func(repo, commit, path string, limit int64) (string, error) {
			b, err := f.Read(ctx, repo, commit, path, limit)
			return string(b), err
		}
		if got, err := read("2001", fx.Head, ".raun.yaml", 1<<16); err != nil || got != "version: 1\n" {
			t.Errorf("Read(.raun.yaml) = %q, %v", got, err)
		}
		if got, err := read("2001", fx.Head, "empty.txt", 1<<16); err != nil || got != "" {
			t.Errorf("Read of an empty file = %q, %v", got, err)
		}
		if got, err := read("2001", fx.Head, "big.txt", 100); err != nil || len(got) != 100 {
			t.Errorf("Read of a file at the bound = %d bytes, %v", len(got), err)
		}
		if got, err := read("2001", fx.Head, "big.txt", 99); got != "" || !errors.Is(err, repository.ErrFileTooLarge) {
			t.Errorf("Read past the bound = %d bytes, %v; want nothing and ErrFileTooLarge", len(got), err)
		}
		for _, tc := range []struct{ name, repo, commit, path string }{
			{"a missing file", "2001", fx.Head, "missing.yaml"},
			{"a directory", "2001", fx.Head, "docs"},
			{"another commit", "2001", strings.Repeat("d", 40), ".raun.yaml"},
			{"another repository", "2002", fx.Head, ".raun.yaml"},
		} {
			if got, err := read(tc.repo, tc.commit, tc.path, 1<<16); got != "" || !errors.Is(err, repository.ErrNoFile) {
				t.Errorf("Read of %s = %q, %v; want ErrNoFile", tc.name, got, err)
			}
		}
		// The App does not reach other/lib: nothing is read.
		if got, err := read("2004", fx.Head, ".raun.yaml", 1<<16); got != "" || err == nil || errors.Is(err, repository.ErrNoFile) {
			t.Errorf("Read of a repository the App does not reach = %q, %v", got, err)
		}
		fx.SetDown(t, true)
		if _, err := read("2001", fx.Head, ".raun.yaml", 1<<16); !errors.Is(err, identity.ErrUnavailable) {
			t.Errorf("Read of a forge down = %v, want ErrUnavailable", err)
		}
	})

	t.Run("events, when the forge has them", func(t *testing.T) {
		fx := newFixture(t)
		e, ok := fx.Forge.(repository.Events)
		if !ok {
			t.Skip("the forge cannot receive events")
		}
		headers, body := fx.Push(t)
		get := func(h map[string]string) func(string) string { return func(name string) string { return h[name] } }
		d, err := e.Parse(get(headers), body)
		if err != nil || d.Forge != fx.Forge.Name() || d.ID == "" || d.Kind != repository.KindPush || d.Repository != "2001" ||
			d.Push == nil || d.Push.Ref != "refs/heads/main" || d.Push.Tag || d.Push.Sender.ID != fx.Alice {
			t.Fatalf("Parse of a push = %+v, %v", d, err)
		}
		// A body changed after signing, and no signature at all, are
		// refused alike.
		tampered := append([]byte(nil), body...)
		tampered[len(tampered)/2] ^= 1
		for name, try := range map[string]func() error{
			"tampered":     func() error { _, err := e.Parse(get(headers), tampered); return err },
			"unsigned":     func() error { _, err := e.Parse(func(string) string { return "" }, body); return err },
			"not a header": func() error { _, err := e.Parse(get(map[string]string{}), body); return err },
		} {
			if err := try(); err != repository.ErrSignature {
				t.Errorf("Parse of a %s delivery = %v, want exactly ErrSignature", name, err)
			}
		}
	})

	t.Run("memberships, when the forge has them", func(t *testing.T) {
		fx := newFixture(t)
		m, ok := identity.MembershipsOf(fx.Forge)
		if !ok {
			t.Skip("the forge has no membership capability")
		}
		_, tokens := login(t, fx)
		if in, err := m.IsMember(ctx, tokens, fx.Org); err != nil || !in {
			t.Errorf("IsMember(Org) = %v, %v; want true", in, err)
		}
		if in, err := m.IsMember(ctx, tokens, fx.OtherOrg); err != nil || in {
			t.Errorf("IsMember(OtherOrg) = %v, %v; want false", in, err)
		}
	})
}
