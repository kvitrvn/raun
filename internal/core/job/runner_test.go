package job_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/job"
	"github.com/kvitrvn/raun/internal/core/job/jobtest"
)

var (
	t0    = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	admin = strings.Repeat("e", 32)
	acme  = job.Workspace{ID: strings.Repeat("a", 32), Name: "acme"}
)

func TestNewRunner(t *testing.T) {
	req := job.RunnerRequest{Name: " builder ", Capabilities: []job.Capability{job.CapabilityNoop, job.CapabilityNoop}, CreatedBy: admin}
	r, token, hash, err := job.NewRunner(rand.Reader, req, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ID) != 32 || r.Name != "builder" || r.Workspace != "" || !slices.Equal(r.Capabilities, []job.Capability{job.CapabilityNoop}) ||
		r.CreatedBy != admin || !r.CreatedAt.Equal(t0) || r.Revoked() || !r.LastContact.IsZero() {
		t.Errorf("NewRunner = %+v", r)
	}
	if len(token) != 64 || hash != job.HashToken(token) {
		t.Errorf("token %q, hash %q", token, hash)
	}
	if c := r.Claimant(); c.Runner != r.ID || c.Workspace != "" || !slices.Equal(c.Capabilities, r.Capabilities) {
		t.Errorf("Claimant = %+v", c)
	}
	for name, change := range map[string]func(*job.RunnerRequest){
		"empty name":         func(r *job.RunnerRequest) { r.Name = "  " },
		"long name":          func(r *job.RunnerRequest) { r.Name = strings.Repeat("a", job.MaxRunnerName+1) },
		"two lines":          func(r *job.RunnerRequest) { r.Name = "a\nb" },
		"invalid UTF-8":      func(r *job.RunnerRequest) { r.Name = "\xff" },
		"bad workspace":      func(r *job.RunnerRequest) { r.Workspace = "acme" },
		"no administrator":   func(r *job.RunnerRequest) { r.CreatedBy = "" },
		"no capability":      func(r *job.RunnerRequest) { r.Capabilities = nil },
		"unknown capability": func(r *job.RunnerRequest) { r.Capabilities = []job.Capability{"agents"} },
	} {
		r := req
		change(&r)
		if _, _, _, err := job.NewRunner(rand.Reader, r, t0); !errors.Is(err, job.ErrInvalidRunner) {
			t.Errorf("%s: NewRunner = %v", name, err)
		}
	}
	if _, _, _, err := job.NewRunner(bytes.NewReader(nil), req, t0); err == nil {
		t.Error("NewRunner without randomness succeeded")
	}
	if got := job.KnownCapabilities(); !slices.Equal(got, []job.Capability{job.CapabilityNoop}) {
		t.Errorf("KnownCapabilities = %v", got)
	}
}

type admins map[string]bool

func (a admins) IsAdministrator(_ context.Context, account string) (bool, error) {
	if account == "broken" {
		return false, errors.New("store down")
	}
	return a[account], nil
}

func TestRegistry(t *testing.T) {
	ctx := context.Background()
	m := &jobtest.Memory{}
	m.AddWorkspace(acme)
	now := t0
	g := job.NewRegistry(job.RegistryOptions{Store: m, Admins: admins{admin: true}, Random: rand.Reader, Now: func() time.Time { return now }})
	noop := []job.Capability{job.CapabilityNoop}

	for _, actor := range []string{"bob", "broken", ""} {
		if _, _, err := g.Create(ctx, actor, "builder", "", noop); err == nil {
			t.Errorf("%q created a runner", actor)
		}
		if _, _, err := g.Runners(ctx, actor); err == nil {
			t.Errorf("%q listed the runners", actor)
		}
	}
	if _, _, err := g.Create(ctx, "bob", "builder", "", noop); !errors.Is(err, job.ErrNotAdministrator) {
		t.Errorf("a non-administrator = %v", err)
	}
	r, token, err := g.Create(ctx, admin, "builder", "acme", noop)
	if err != nil || r.Workspace != acme.ID || r.CreatedBy != admin {
		t.Fatalf("Create = %+v, %v", r, err)
	}
	if _, _, err := g.Create(ctx, admin, "other", "nowhere", noop); !errors.Is(err, job.ErrNoWorkspace) {
		t.Errorf("an unknown workspace = %v", err)
	}
	if _, _, err := g.Create(ctx, admin, "builder", "", noop); !errors.Is(err, job.ErrRunnerNameTaken) {
		t.Errorf("a taken name = %v", err)
	}
	runners, workspaces, err := g.Runners(ctx, admin)
	if err != nil || len(runners) != 1 || len(workspaces) != 1 {
		t.Errorf("Runners = %+v, %+v, %v", runners, workspaces, err)
	}
	if got, err := g.Authenticate(ctx, token, "1.0.0"); err != nil || got.ID != r.ID {
		t.Errorf("Authenticate = %+v, %v", got, err)
	}
	for _, bad := range []string{"", strings.Repeat("a", job.MaxToken+1), "nope"} {
		if _, err := g.Authenticate(ctx, bad, "1.0.0"); !errors.Is(err, job.ErrUnauthenticated) {
			t.Errorf("Authenticate(%.8q) = %v", bad, err)
		}
	}
	if _, _, err := g.Rotate(ctx, "bob", r.ID); !errors.Is(err, job.ErrNotAdministrator) {
		t.Errorf("Rotate by a non-administrator = %v", err)
	}
	_, fresh, err := g.Rotate(ctx, admin, r.ID)
	if err != nil || fresh == token {
		t.Fatalf("Rotate = %v", err)
	}
	if _, err := g.Authenticate(ctx, token, "1.0.0"); !errors.Is(err, job.ErrUnauthenticated) {
		t.Errorf("the old token = %v", err)
	}
	if _, _, err := g.Revoke(ctx, "bob", r.ID); !errors.Is(err, job.ErrNotAdministrator) {
		t.Errorf("Revoke by a non-administrator = %v", err)
	}
	now = t0.Add(time.Hour)
	got, _, err := g.Revoke(ctx, admin, r.ID)
	if err != nil || !got.RevokedAt.Equal(now) {
		t.Errorf("Revoke = %+v, %v", got, err)
	}
	if _, err := g.Authenticate(ctx, fresh, "1.0.0"); !errors.Is(err, job.ErrUnauthenticated) {
		t.Errorf("the token of a revoked runner = %v", err)
	}
}
