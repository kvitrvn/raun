package knowledge

import (
	"errors"
	"fmt"
	"testing"
)

var (
	human = Actor{Kind: ActorHuman, Name: testPerson}
	run   = Actor{Kind: ActorRun, Name: testRun}
)

func TestTransitionMatrix(t *testing.T) {
	// Every (from, to, actor) combination: allowed exactly when listed here.
	allowed := map[string]bool{
		"proposed>contested:run":       true,
		"proposed>validated:human":     true,
		"proposed>rejected:human":      true,
		"contested>proposed:human":     true,
		"contested>validated:human":    true,
		"contested>rejected:human":     true,
		"validated>needs-review:run":   true,
		"validated>rejected:human":     true,
		"needs-review>validated:human": true,
		"needs-review>rejected:human":  true,
		"rejected>proposed:human":      true,
	}
	for _, from := range Statuses {
		for _, to := range Statuses {
			for _, actor := range []Actor{human, run} {
				key := fmt.Sprintf("%s>%s:%s", from, to, actor.Kind)
				t.Run(key, func(t *testing.T) {
					it := samplePersona()
					it.Status = from
					err := it.Transition(to, actor, testTime, "reviewed")
					if allowed[key] {
						if err != nil {
							t.Fatalf("Transition() error = %v, want allowed", err)
						}
						last := it.History[len(it.History)-1]
						if it.Status != to || last.From != from || last.To != to || last.By != actor.Name || last.Actor != actor.Kind {
							t.Errorf("history not recorded correctly: status=%s last=%+v", it.Status, last)
						}
						return
					}
					if !errors.Is(err, ErrTransition) {
						t.Errorf("Transition() error = %v, want ErrTransition", err)
					}
					if it.Status != from || len(it.History) != 1 {
						t.Errorf("refused transition modified the item")
					}
				})
			}
		}
	}
}

func TestRunsNeverValidateOrReject(t *testing.T) {
	for _, from := range Statuses {
		for _, to := range []Status{StatusValidated, StatusRejected} {
			it := samplePersona()
			it.Status = from
			if err := it.Transition(to, run, testTime, ""); err == nil {
				t.Errorf("run moved %s -> %s", from, to)
			}
		}
	}
}

func TestTransitionPreconditions(t *testing.T) {
	t.Run("human needs a reason", func(t *testing.T) {
		it := samplePersona()
		if err := it.Transition(StatusValidated, human, testTime, " "); !errors.Is(err, ErrTransition) {
			t.Errorf("error = %v, want ErrTransition", err)
		}
	})
	t.Run("actor needs a name", func(t *testing.T) {
		it := samplePersona()
		if err := it.Transition(StatusContested, Actor{Kind: ActorRun}, testTime, ""); !errors.Is(err, ErrTransition) {
			t.Errorf("error = %v, want ErrTransition", err)
		}
	})
	t.Run("open disagreement blocks validation", func(t *testing.T) {
		it := sampleRequirement()
		for _, to := range []Status{StatusValidated, StatusProposed} {
			if err := it.Transition(to, human, testTime, "ok"); !errors.Is(err, ErrTransition) {
				t.Errorf("contested -> %s: error = %v, want ErrTransition", to, err)
			}
		}
		if err := it.Transition(StatusRejected, human, testTime, "wrong"); err != nil {
			t.Errorf("rejecting must stay possible: %v", err)
		}
	})
	t.Run("resolved disagreement allows validation", func(t *testing.T) {
		it := sampleRequirement()
		resolveAll(it)
		if err := it.Transition(StatusValidated, human, testTime, "code is authoritative"); err != nil {
			t.Fatalf("error = %v", err)
		}
		if err := it.Validate(); err != nil {
			t.Errorf("item invalid after transition: %v", err)
		}
	})
}
