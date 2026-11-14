package repository_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvitrvn/raun/internal/core/repository"
	"github.com/kvitrvn/raun/internal/core/repository/repositorytest"
)

// stubForge lists repositories and gives permissions per account; err, when
// set, is the answer of every call.
type stubForge struct {
	mu     sync.Mutex
	repos  map[string]repository.Listed                // identifier -> repository
	rights map[string]map[string]repository.Permission // account -> identifier -> permission
	err    error
	calls  int
}

func (f *stubForge) Name() string { return "github" }

func (f *stubForge) Repositories(_ context.Context, account string) ([]repository.Listed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	var out []repository.Listed
	for id, p := range f.rights[account] {
		if r, ok := f.repos[id]; ok && p != repository.PermissionNone {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b repository.Listed) int { return strings.Compare(a.FullName, b.FullName) })
	return out, nil
}

func (f *stubForge) Permission(_ context.Context, account, id string) (repository.Permission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return repository.PermissionNone, f.err
	}
	if _, ok := f.repos[id]; !ok {
		return repository.PermissionNone, nil
	}
	if p, ok := f.rights[account][id]; ok {
		return p, nil
	}
	return repository.PermissionNone, nil
}

func (f *stubForge) grant(account, id string, p repository.Permission) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rights[account] == nil {
		f.rights[account] = map[string]repository.Permission{}
	}
	f.rights[account][id] = p
}

func (f *stubForge) rename(id, fullName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[id]
	r.FullName = fullName
	f.repos[id] = r
}

func (f *stubForge) remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.repos, id)
}

func (f *stubForge) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// Reached gives every repository the stub lists, or err.
func (f *stubForge) Reached(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var out []string
	for id := range f.repos {
		out = append(out, id)
	}
	return out, nil
}

// stubWorkspaces: acme has Alice (owner) and Carol, beta has Bob.
type stubWorkspaces struct {
	err error
}

var (
	acme = repository.Workspace{ID: "w-acme", Name: "acme"}
	beta = repository.Workspace{ID: "w-beta", Name: "beta"}
)

func (w *stubWorkspaces) WorkspaceOf(_ context.Context, name, account string) (repository.Workspace, error) {
	if w.err != nil {
		return repository.Workspace{}, w.err
	}
	members := map[string][]string{"acme": {"alice", "carol"}, "beta": {"bob"}}
	if !slices.Contains(members[name], account) {
		return repository.Workspace{}, repository.ErrNotMember
	}
	return map[string]repository.Workspace{"acme": acme, "beta": beta}[name], nil
}

type world struct {
	forge      *stubForge
	workspaces *stubWorkspaces
	store      *repositorytest.Memory
	service    *repository.Service
	now        time.Time
}

// newWorld: acme/api (2001), where Alice and Bob decide, Carol views, and
// Dave decides without belonging to a workspace; acme/web (2002), where
// Alice views.
func newWorld(t *testing.T, missing ...repository.Capability) *world {
	t.Helper()
	w := &world{
		forge: &stubForge{repos: map[string]repository.Listed{
			"2001": {Forge: "github", ID: "2001", FullName: "acme/api", DefaultBranch: "main", Private: true},
			"2002": {Forge: "github", ID: "2002", FullName: "acme/web", DefaultBranch: "main"},
		}, rights: map[string]map[string]repository.Permission{}},
		workspaces: &stubWorkspaces{},
		store:      &repositorytest.Memory{},
		now:        time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC),
	}
	w.store.AddWorkspace(acme)
	w.store.AddWorkspace(beta)
	w.forge.grant("alice", "2001", repository.PermissionDecide)
	w.forge.grant("alice", "2002", repository.PermissionView)
	w.forge.grant("bob", "2001", repository.PermissionDecide)
	w.forge.grant("carol", "2001", repository.PermissionView)
	w.forge.grant("dave", "2001", repository.PermissionDecide)
	w.service = repository.New(repository.Options{
		Forge: w.forge, Workspaces: w.workspaces, Store: w.store, Reach: w.forge,
		Missing: missing, Now: func() time.Time { return w.now },
	})
	return w
}

func (w *world) stored(t *testing.T, id string) (repository.Repository, bool) {
	t.Helper()
	got, err := w.store.Known(context.Background(), "github", []string{id})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		return repository.Repository{}, false
	}
	return got[0], true
}

func TestActivate(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	r, err := w.service.Activate(ctx, "alice", "acme", "2001")
	if err != nil {
		t.Fatal(err)
	}
	want := repository.Repository{
		Forge: "github", ID: "2001", Workspace: acme, FullName: "acme/api", DefaultBranch: "main", Private: true,
		Active: true, Reachable: true, ChangedBy: "alice", ChangedAt: w.now,
	}
	if r != want {
		t.Errorf("Activate = %+v, want %+v", r, want)
	}
	if got, ok := w.stored(t, "2001"); !ok || got != want {
		t.Errorf("stored %+v, want %+v", got, want)
	}
}

func TestActivationRefusals(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name            string
		account, ws, id string
		missing         []repository.Capability
		want            error
	}{
		{"a stranger to the workspace", "dave", "acme", "2001", nil, repository.ErrNotMember},
		{"an unknown workspace", "alice", "nowhere", "2001", nil, repository.ErrNotMember},
		{"a member who views", "carol", "acme", "2001", nil, repository.ErrNotDecider},
		{"a member who views only this one", "alice", "acme", "2002", nil, repository.ErrNotDecider},
		{"a repository the forge does not list", "alice", "acme", "2003", nil, repository.ErrNotFound},
		{"no identifier", "alice", "acme", "", nil, repository.ErrNotFound},
		{"a forge without files", "alice", "acme", "2001", []repository.Capability{repository.CapabilityFiles}, repository.ErrMissingCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, tc.missing...)
			if _, err := w.service.Activate(ctx, tc.account, tc.ws, tc.id); !errors.Is(err, tc.want) {
				t.Errorf("Activate = %v, want %v", err, tc.want)
			}
			if _, ok := w.stored(t, tc.id); ok {
				t.Error("a refused activation stored the repository")
			}
		})
	}
}

func TestMissingCapabilitiesAreNamed(t *testing.T) {
	w := newWorld(t, repository.CapabilityWebhooks, repository.CapabilityEvents)
	_, err := w.service.Activate(context.Background(), "alice", "acme", "2001")
	var missing *repository.MissingError
	if !errors.As(err, &missing) || len(missing.Missing) != 2 || !strings.Contains(err.Error(), "webhooks, events") {
		t.Fatalf("Activate = %v, want the missing capabilities named", err)
	}
	// Refused before the forge is asked anything.
	if w.forge.calls != 0 {
		t.Errorf("%d calls to the forge", w.forge.calls)
	}
	if got := w.service.Missing(); len(got) != 2 {
		t.Errorf("Missing = %v", got)
	}
}

func TestOneWorkspaceForGood(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if _, err := w.service.Activate(ctx, "alice", "acme", "2001"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.Activate(ctx, "bob", "beta", "2001"); !errors.Is(err, repository.ErrElsewhere) {
		t.Fatalf("activation in beta: %v, want ErrElsewhere", err)
	}
	if _, err := w.service.Deactivate(ctx, "alice", "acme", "acme/api"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.Activate(ctx, "bob", "beta", "2001"); !errors.Is(err, repository.ErrElsewhere) {
		t.Errorf("activation in beta of a deactivated repository: %v, want ErrElsewhere", err)
	}
}

func TestDeactivateAndReactivate(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if _, err := w.service.Activate(ctx, "alice", "acme", "2001"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, account, ws, fullName string
		want                        error
	}{
		{"by a stranger", "bob", "acme", "acme/api", repository.ErrNotMember},
		{"by a member who views", "carol", "acme", "acme/api", repository.ErrNotDecider},
		{"in another workspace", "bob", "beta", "acme/api", repository.ErrNotFound},
		{"of an unknown repository", "alice", "acme", "acme/nothing", repository.ErrNotFound},
		{"without a name", "alice", "acme", "", repository.ErrNotFound},
	} {
		if _, err := w.service.Deactivate(ctx, tc.account, tc.ws, tc.fullName); !errors.Is(err, tc.want) {
			t.Errorf("deactivation %s: %v, want %v", tc.name, err, tc.want)
		}
	}
	if got, _ := w.stored(t, "2001"); !got.Active {
		t.Fatal("a refused deactivation deactivated")
	}
	w.now = w.now.Add(time.Hour)
	r, err := w.service.Deactivate(ctx, "alice", "acme", "acme/api")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := w.stored(t, "2001"); got != r || got.State() != repository.StateDeactivated || !got.ChangedAt.Equal(w.now) {
		t.Errorf("deactivated: %+v, returned %+v", got, r)
	}
	// Still readable by whoever the forge lets read it.
	if v, err := w.service.Open(ctx, "carol", "acme", "acme/api"); err != nil || v.Repository.Active {
		t.Errorf("Open of a deactivated repository = %+v, %v", v, err)
	}
	if _, err := w.service.Activate(ctx, "alice", "acme", "2001"); err != nil {
		t.Fatalf("reactivation: %v", err)
	}
	if got, _ := w.stored(t, "2001"); got.State() != repository.StateActive || got.Workspace != acme {
		t.Errorf("reactivated: %+v", got)
	}
}

func TestOpen(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if _, err := w.service.Activate(ctx, "alice", "acme", "2001"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		account        string
		member, decide bool
	}{
		{"alice", true, true},
		{"carol", true, false},
		// The forge decides who sees it: a stranger to the workspace who
		// can read the repository sees its page, and cannot change it.
		{"bob", false, false},
	} {
		v, err := w.service.Open(ctx, tc.account, "acme", "acme/api")
		if err != nil || v.Repository.ID != "2001" || v.Member != tc.member || v.CanDecide() != tc.decide {
			t.Errorf("Open by %s = %+v, %v", tc.account, v, err)
		}
	}
	for _, tc := range []struct{ account, ws, fullName string }{
		{"eve", "acme", "acme/api"},   // the forge hides it
		{"alice", "beta", "acme/api"}, // not in that workspace
		{"alice", "acme", "acme/web"}, // never activated
		{"alice", "acme", "ACME/api"}, // the name as stored only
		{"alice", "acme", ""},         // no name
	} {
		if _, err := w.service.Open(ctx, tc.account, tc.ws, tc.fullName); !errors.Is(err, repository.ErrNotFound) {
			t.Errorf("Open(%s, %s, %s) = %v, want ErrNotFound", tc.account, tc.ws, tc.fullName, err)
		}
	}
	w.forge.setErr(repository.ErrUnavailable)
	if _, err := w.service.Open(ctx, "alice", "acme", "acme/api"); !errors.Is(err, repository.ErrUnavailable) {
		t.Errorf("Open with the forge down = %v, want ErrUnavailable", err)
	}
	w.forge.setErr(nil)
	w.workspaces.err = errors.New("disk")
	if _, err := w.service.Open(ctx, "alice", "acme", "acme/api"); err == nil || errors.Is(err, repository.ErrNotFound) {
		t.Errorf("Open with the workspaces failing = %v, want an error", err)
	}
}

// TestARenamedRepository: a rename on the forge keeps the activation; the
// page answers at the new name only, once a listing brought it.
func TestARenamedRepository(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if _, err := w.service.Activate(ctx, "alice", "acme", "2001"); err != nil {
		t.Fatal(err)
	}
	w.forge.rename("2001", "acme/backend")
	entries, err := w.service.List(ctx, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Listed.FullName != "acme/backend" || !entries[0].Known || entries[0].Repository.FullName != "acme/backend" {
		t.Fatalf("List after a rename = %+v", entries)
	}
	if _, err := w.service.Open(ctx, "alice", "acme", "acme/api"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("Open at the old name = %v, want ErrNotFound", err)
	}
	v, err := w.service.Open(ctx, "alice", "acme", "acme/backend")
	if err != nil || v.Repository.ID != "2001" || v.Repository.ChangedBy != "alice" || !v.Repository.Active {
		t.Errorf("Open at the new name = %+v, %v", v, err)
	}
}

func TestList(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if _, err := w.service.Activate(ctx, "bob", "beta", "2001"); err != nil {
		t.Fatal(err)
	}
	entries, err := w.service.List(ctx, "alice", "")
	if err != nil || len(entries) != 2 {
		t.Fatalf("List = %+v, %v", entries, err)
	}
	if e := entries[0]; e.Listed.ID != "2001" || !e.Known || e.Repository.Workspace != beta {
		t.Errorf("acme/api: %+v", e)
	}
	if e := entries[1]; e.Listed.ID != "2002" || e.Known {
		t.Errorf("acme/web: %+v", e)
	}
	for query, want := range map[string]string{"WEB": "acme/web", " api ": "acme/api", "acme": "acme/api acme/web", "nothing": ""} {
		entries, err := w.service.List(ctx, "alice", query)
		var names []string
		for _, e := range entries {
			names = append(names, e.Listed.FullName)
		}
		if err != nil || strings.Join(names, " ") != want {
			t.Errorf("List(%q) = %v, %v; want %q", query, names, err, want)
		}
	}
	// Each person sees the repositories the forge lists for them.
	if entries, err := w.service.List(ctx, "carol", ""); err != nil || len(entries) != 1 {
		t.Errorf("List(carol) = %+v, %v", entries, err)
	}
	w.forge.setErr(repository.ErrRefused)
	if _, err := w.service.List(ctx, "alice", ""); !errors.Is(err, repository.ErrRefused) {
		t.Errorf("List with refused tokens = %v", err)
	}
}

func TestInWorkspace(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if _, err := w.service.Activate(ctx, "alice", "acme", "2001"); err != nil {
		t.Fatal(err)
	}
	if got, err := w.service.InWorkspace(ctx, "alice", "acme"); err != nil || len(got) != 1 || got[0].ID != "2001" {
		t.Errorf("InWorkspace(alice, acme) = %+v, %v", got, err)
	}
	// A member the forge hides the repository from does not learn of it.
	w.forge.grant("carol", "2001", repository.PermissionNone)
	if got, err := w.service.InWorkspace(ctx, "carol", "acme"); err != nil || len(got) != 0 {
		t.Errorf("InWorkspace(carol, acme) = %+v, %v", got, err)
	}
	if _, err := w.service.InWorkspace(ctx, "bob", "acme"); !errors.Is(err, repository.ErrNotMember) {
		t.Errorf("InWorkspace(bob, acme) = %v, want ErrNotMember", err)
	}
}

// TestTheAppLosesARepository: a repository the App no longer reaches is
// deactivated, and can be activated again once the App reaches it.
func TestTheAppLosesARepository(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if _, err := w.service.Activate(ctx, "alice", "acme", "2001"); err != nil {
		t.Fatal(err)
	}
	// A forge that cannot answer changes nothing.
	w.forge.setErr(repository.ErrUnavailable)
	if _, err := w.service.SyncReach(ctx); !errors.Is(err, repository.ErrUnavailable) {
		t.Fatalf("SyncReach with the forge down = %v", err)
	}
	if got, _ := w.stored(t, "2001"); got.State() != repository.StateActive {
		t.Fatalf("a failed answer changed the state: %+v", got)
	}
	w.forge.setErr(nil)
	w.forge.remove("2001")
	lost, err := w.service.SyncReach(ctx)
	if err != nil || strings.Join(lost, " ") != "2001" {
		t.Fatalf("SyncReach = %v, %v", lost, err)
	}
	if got, _ := w.stored(t, "2001"); got.State() != repository.StateUnreachable || got.Active {
		t.Errorf("after the loss: %+v", got)
	}
	// Back on the forge: reachable, still deactivated, until activated.
	w.forge.repos["2001"] = repository.Listed{Forge: "github", ID: "2001", FullName: "acme/api", DefaultBranch: "main"}
	if _, err := w.service.SyncReach(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.stored(t, "2001"); got.State() != repository.StateDeactivated {
		t.Errorf("reached again: %+v, want deactivated", got)
	}
	if _, err := w.service.Activate(ctx, "alice", "acme", "2001"); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.stored(t, "2001"); got.State() != repository.StateActive {
		t.Errorf("activated again: %+v", got)
	}
}

func TestSyncReachWithoutTheCapability(t *testing.T) {
	s := repository.New(repository.Options{Forge: &stubForge{}, Workspaces: &stubWorkspaces{}, Store: &repositorytest.Memory{}})
	if _, err := s.SyncReach(context.Background()); !errors.Is(err, repository.ErrNoReach) {
		t.Errorf("SyncReach = %v, want ErrNoReach", err)
	}
}

func TestForgeErrorsLeaveTheStateAlone(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if _, err := w.service.Activate(ctx, "alice", "acme", "2001"); err != nil {
		t.Fatal(err)
	}
	w.forge.setErr(repository.ErrUnavailable)
	if _, err := w.service.Deactivate(ctx, "alice", "acme", "acme/api"); !errors.Is(err, repository.ErrUnavailable) {
		t.Errorf("Deactivate with the forge down = %v", err)
	}
	if _, err := w.service.Activate(ctx, "alice", "acme", "2002"); !errors.Is(err, repository.ErrUnavailable) {
		t.Errorf("Activate with the forge down = %v", err)
	}
	if got, _ := w.stored(t, "2001"); got.State() != repository.StateActive {
		t.Errorf("state after the failures: %+v", got)
	}
}

// stubEvents parses the deliveries scripted by body; a delivery is signed
// when its signature header says "good".
type stubEvents struct {
	deliveries map[string]repository.Delivery
	parsed     int
}

func (e *stubEvents) Parse(header func(string) string, body []byte) (repository.Delivery, error) {
	if header("Signature") != "good" {
		return repository.Delivery{}, repository.ErrSignature
	}
	e.parsed++
	d, ok := e.deliveries[string(body)]
	if !ok {
		return repository.Delivery{}, repository.ErrMalformed
	}
	d.ID = header("Delivery")
	return d, nil
}

func signed(delivery string) func(string) string {
	return func(name string) string {
		return map[string]string{"Signature": "good", "Delivery": delivery}[name]
	}
}

func pushTo(repo, after string) repository.Delivery {
	return repository.Delivery{Forge: "github", Event: "push", Kind: repository.KindPush, Repository: repo,
		Push: &repository.Push{Ref: "refs/heads/main", Before: "b0", After: after, Head: after}}
}

func newReceiving(t *testing.T) (*world, *stubEvents) {
	t.Helper()
	w := newWorld(t)
	events := &stubEvents{deliveries: map[string]repository.Delivery{
		"push api":     pushTo("2001", "c1"),
		"push api c0":  pushTo("2001", "c0"),
		"push web":     pushTo("2002", "c1"),
		"push nowhere": pushTo("9999", "c1"),
		"pr closed": {Forge: "github", Event: "pull_request", Action: "closed", Kind: repository.KindPullRequest, Repository: "2001",
			PullRequest: &repository.PullRequest{Number: 7, Action: "closed", HeadRef: "feature", HeadCommit: "h1", BaseRef: "main", BaseCommit: "b1", HeadRepository: "2001", BaseRepository: "2001"}},
		"pr reopened": {Forge: "github", Event: "pull_request", Action: "reopened", Kind: repository.KindPullRequest, Repository: "2001",
			PullRequest: &repository.PullRequest{Number: 7, Action: "reopened", HeadRef: "feature", HeadCommit: "h1", BaseRef: "main", BaseCommit: "b1", HeadRepository: "2001", BaseRepository: "2001"}},
		"issues":       {Forge: "github", Event: "issues", Action: "opened", Kind: repository.KindIgnored, Repository: "2001"},
		"ping":         {Forge: "github", Event: "ping", Kind: repository.KindIgnored},
		"installation": {Forge: "github", Event: "installation", Action: "deleted", Kind: repository.KindInstallation},
	}}
	w.service = repository.New(repository.Options{
		Forge: w.forge, Workspaces: w.workspaces, Store: w.store, Reach: w.forge,
		Events: events, Log: w.store, Now: func() time.Time { return w.now },
	})
	if _, err := w.service.Activate(context.Background(), "alice", "acme", "2001"); err != nil {
		t.Fatal(err)
	}
	return w, events
}

func TestReceive(t *testing.T) {
	ctx := context.Background()
	w, events := newReceiving(t)
	for _, tc := range []struct {
		delivery, body string
		want           repository.Outcome
	}{
		{"d1", "push api", repository.OutcomeRecorded},
		{"d1", "push api c0", repository.OutcomeDuplicate}, // sent again
		{"d2", "push api", repository.OutcomeDuplicate},    // replayed under a new identifier
		{"d3", "push api c0", repository.OutcomeRecorded},  // a push back to a commit
		{"d4", "pr closed", repository.OutcomeRecorded},    // closed, then reopened at the same head:
		{"d5", "pr reopened", repository.OutcomeRecorded},  // both kept
		{"d6", "push web", repository.OutcomeDropped},      // never activated
		{"d7", "push nowhere", repository.OutcomeDropped},  // unknown
		{"d8", "issues", repository.OutcomeIgnored},        // logged as ignored
		{"d9", "ping", repository.OutcomeDropped},          // of no repository
		{"d10", "installation", repository.OutcomeInstallation},
		{"d11", "installation", repository.OutcomeDuplicate}, // a replayed installation delivery
	} {
		w.now = w.now.Add(time.Second)
		got, err := w.service.Receive(ctx, signed(tc.delivery), []byte(tc.body))
		if err != nil || got != tc.want {
			t.Errorf("Receive(%s, %q) = %v, %v; want %v", tc.delivery, tc.body, got, err, tc.want)
		}
	}
	v, err := w.service.Open(ctx, "alice", "acme", "acme/api")
	if err != nil {
		t.Fatal(err)
	}
	var log []string
	for _, e := range v.Events {
		log = append(log, e.Delivery+":"+string(e.Result))
	}
	if strings.Join(log, " ") != "d8:ignored d5:recorded d4:recorded d3:recorded d1:recorded" {
		t.Errorf("the event log = %v", log)
	}
	if e := v.Events[1]; e.Event != "pull_request" || e.Action != "reopened" || e.Ref != "feature" || e.Before != "b1" || e.After != "h1" {
		t.Errorf("the row of a pull request = %+v", e)
	}
	if e := v.Events[4]; e.Ref != "refs/heads/main" || e.Before != "b0" || e.After != "c1" || !e.ReceivedAt.Equal(time.Date(2026, 11, 14, 10, 0, 1, 0, time.UTC)) {
		t.Errorf("the row of a push = %+v", e)
	}
	if events.parsed != 12 {
		t.Errorf("%d deliveries parsed", events.parsed)
	}
}

func TestReceiveRefusals(t *testing.T) {
	ctx := context.Background()
	w, _ := newReceiving(t)
	unsigned := func(string) string { return "" }
	if _, err := w.service.Receive(ctx, unsigned, []byte("push api")); !errors.Is(err, repository.ErrSignature) {
		t.Errorf("an unsigned delivery = %v", err)
	}
	if _, err := w.service.Receive(ctx, signed("d1"), []byte("garbage")); !errors.Is(err, repository.ErrMalformed) {
		t.Errorf("a malformed delivery = %v", err)
	}
	// Neither kept a key: the same delivery, signed, is recorded.
	if got, err := w.service.Receive(ctx, signed("d1"), []byte("push api")); err != nil || got != repository.OutcomeRecorded {
		t.Errorf("after the refusals = %v, %v", got, err)
	}
	none := repository.New(repository.Options{Forge: w.forge, Workspaces: w.workspaces, Store: w.store})
	if _, err := none.Receive(ctx, signed("d1"), []byte("push api")); !errors.Is(err, repository.ErrNoEvents) {
		t.Errorf("Receive without the capability = %v", err)
	}
}

// TestReceiveAfterADeactivation: the deliveries of a deactivated repository
// keep their keys and log nothing; its log stays readable.
func TestReceiveAfterADeactivation(t *testing.T) {
	ctx := context.Background()
	w, _ := newReceiving(t)
	if _, err := w.service.Receive(ctx, signed("d1"), []byte("push api")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.Deactivate(ctx, "alice", "acme", "acme/api"); err != nil {
		t.Fatal(err)
	}
	if got, err := w.service.Receive(ctx, signed("d2"), []byte("push api c0")); err != nil || got != repository.OutcomeDropped {
		t.Errorf("a push to a deactivated repository = %v, %v", got, err)
	}
	if v, err := w.service.Open(ctx, "carol", "acme", "acme/api"); err != nil || len(v.Events) != 1 {
		t.Errorf("the log of a deactivated repository = %+v, %v", v.Events, err)
	}
}
