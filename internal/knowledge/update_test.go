package knowledge

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func revisionFor(t *testing.T, it *Item) string {
	t.Helper()
	revision, err := Revision(it)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestUpdateDecisionAndConflicts(t *testing.T) {
	s := NewStore(t.TempDir())
	it := samplePersona()
	if err := s.Save(it); err != nil {
		t.Fatal(err)
	}
	revision := revisionFor(t, it)
	before, _ := Encode(it)
	const workers = 12
	results := make(chan error, workers)
	var start sync.WaitGroup
	start.Add(1)
	for range workers {
		go func() {
			start.Wait()
			results <- NewStore(s.dir).Update(it.ID, revision, func(current *Item) error {
				return current.Transition(StatusValidated, Actor{Kind: ActorHuman, Name: "Reviewer"}, testTime, "Confirmed")
			})
		}()
	}
	start.Done()
	successes := 0
	for range workers {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful updates = %d", successes)
	}
	got, err := s.Get(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusValidated || len(got.History) != len(it.History)+1 {
		t.Fatalf("item after updates: %+v", got)
	}
	last := got.History[len(got.History)-1]
	if last.Actor != ActorHuman || last.By != "Reviewer" || last.Reason != "Confirmed" {
		t.Fatalf("event: %+v", last)
	}
	got.Status, got.History = it.Status, it.History
	after, _ := Encode(got)
	if !bytes.Equal(before, after) {
		t.Fatal("decision changed business content, interpretations, evidence or open points")
	}
	entries, err := os.ReadDir(filepath.Dir(s.Path(it.Type, it.ID)))
	if err != nil || len(entries) != 1 {
		t.Fatalf("lock or temporary files remain: %v, %v", entries, err)
	}
}

func TestUpdateFailureReleasesLock(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*Item) error
	}{
		{"callback", func(it *Item) error { it.Title = "Changed"; return errors.New("stop") }},
		{"validation", func(it *Item) error { it.Title = ""; return nil }},
		{"identity", func(it *Item) error { it.ID = "persona-aaaaaa"; return nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := NewStore(t.TempDir())
			it := samplePersona()
			if err := s.Save(it); err != nil {
				t.Fatal(err)
			}
			revision := revisionFor(t, it)
			if err := s.Update(it.ID, revision, tt.mutate); err == nil {
				t.Fatal("accepted failed mutation")
			}
			got, err := s.Get(it.ID)
			if err != nil {
				t.Fatal(err)
			}
			if revisionFor(t, got) != revision {
				t.Fatal("failed mutation persisted")
			}
			if err := s.Update(it.ID, revision, func(*Item) error { return nil }); err != nil {
				t.Fatalf("lock not released: %v", err)
			}
		})
	}
}

func TestUpdateMissingInvalidAndRevision(t *testing.T) {
	s := NewStore(t.TempDir())
	it := samplePersona()
	for _, id := range []string{it.ID, "../escape"} {
		if err := s.Update(id, "", func(*Item) error { t.Fatal("called mutation"); return nil }); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing: %v", err)
		}
	}
	if err := s.Save(it); err != nil {
		t.Fatal(err)
	}
	p := s.Path(it.Type, it.ID)
	revision := revisionFor(t, it)
	original, _ := os.ReadFile(p)
	// Formatting changes do not change the canonical revision.
	if err := os.WriteFile(p, append([]byte("# comment\n"), original...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(it.ID, revision, func(*Item) error { return nil }); err != nil {
		t.Fatal(err)
	}
	it.Evidence[0].State = EvidenceStale
	if err := s.Save(it); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(it.ID, revision, func(*Item) error { t.Fatal("stale mutation called"); return nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale evidence: %v", err)
	}
	if err := os.WriteFile(p, []byte("unknown: invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(it.ID, revision, func(*Item) error { return nil }); err == nil {
		t.Fatal("invalid YAML accepted")
	}
	if err := s.Save(it); err != nil {
		t.Fatalf("invalid read left lock: %v", err)
	}
}

// The subprocess holds a real file lock while the parent attempts another write.
func TestUpdateLockProcess(t *testing.T) {
	dir := os.Getenv("RAUN_TEST_UPDATE_DIR")
	if dir == "" {
		return
	}
	s := NewStore(dir)
	it, err := s.Get("persona-k3x9q2")
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(it.ID, revisionFor(t, it), func(current *Item) error {
		fmt.Println("LOCKED")
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			return errors.New("parent did not release writer")
		}
		return current.Transition(StatusValidated, Actor{Kind: ActorHuman, Name: "Child"}, testTime, "Cross-process check")
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpdateAcrossProcesses(t *testing.T) {
	s := NewStore(t.TempDir())
	it := samplePersona()
	if err := s.Save(it); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestUpdateLockProcess$")
	cmd.Env = append(os.Environ(), "RAUN_TEST_UPDATE_DIR="+s.dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "LOCKED" {
		t.Fatalf("child did not lock: %s", stderr.String())
	}
	revision := revisionFor(t, it)
	if err := s.Update(it.ID, revision, func(*Item) error { t.Fatal("concurrent mutation ran"); return nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("concurrent update: %v", err)
	}
	if err := s.Save(it); !errors.Is(err, ErrConflict) {
		t.Fatalf("concurrent Save: %v", err)
	}
	if _, err := fmt.Fprintln(stdin, "release"); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v, %s", err, stderr.String())
	}
	if err := s.Update(it.ID, revision, func(*Item) error { return nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale after child: %v", err)
	}
	got, err := s.Get(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusValidated {
		t.Fatal("child decision missing")
	}
	if err := s.Update(it.ID, revisionFor(t, got), func(*Item) error { return nil }); err != nil {
		t.Fatalf("child left lock: %v", err)
	}
}
