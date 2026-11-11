package knowledge

import (
	"errors"
	"testing"
	"time"
)

// wantTransitions is the table of docs/format.md, written again here so that
// a change of the table in the code fails the test.
var wantTransitions = map[[2]Status]ActorKind{
	{"", StatusProposed}:                 ActorRun,
	{StatusProposed, StatusContested}:    ActorRun,
	{StatusProposed, StatusValidated}:    ActorHuman,
	{StatusProposed, StatusRejected}:     ActorHuman,
	{StatusContested, StatusProposed}:    ActorHuman,
	{StatusContested, StatusRejected}:    ActorHuman,
	{StatusValidated, StatusNeedsReview}: ActorRun,
	{StatusValidated, StatusRejected}:    ActorHuman,
	{StatusNeedsReview, StatusValidated}: ActorHuman,
	{StatusNeedsReview, StatusRejected}:  ActorHuman,
	{StatusRejected, StatusProposed}:     ActorHuman,
	{StatusRejected, StatusContested}:    ActorHuman,
}

var allStatuses = []Status{StatusProposed, StatusContested, StatusValidated, StatusRejected, StatusNeedsReview}

// matrixFixture returns an item in status from that meets the condition of
// the row from -> to, so that only the table decides.
func matrixFixture(t *testing.T, from, to Status) Item {
	t.Helper()
	switch from {
	case "":
		return Item{ID: testReqID}
	case StatusContested:
		it := itemIn(t, StatusContested, true)
		if to == StatusProposed {
			// The resolution path is tested on its own; here only the row.
			it.OpenPoints[0].Resolution = &Resolution{At: at(6), By: testHuman, Note: "the docs are outdated"}
		}
		return it
	case StatusRejected:
		return itemIn(t, StatusRejected, to == StatusContested)
	case StatusProposed:
		it := itemIn(t, StatusProposed, false)
		if to == StatusContested {
			// AppendFromRun appends the disagreement before the transition.
			it.OpenPoints = append(it.OpenPoints, disagreement("p1", testRun))
		}
		return it
	default:
		return itemIn(t, from, false)
	}
}

func TestTransitionMatrix(t *testing.T) {
	froms := append([]Status{""}, allStatuses...)
	for _, from := range froms {
		for _, to := range allStatuses {
			for _, actor := range []ActorKind{ActorHuman, ActorRun} {
				name := string(from) + "->" + string(to) + " by " + string(actor)
				if from == "" {
					name = "creation->" + string(to) + " by " + string(actor)
				}
				t.Run(name, func(t *testing.T) {
					it := matrixFixture(t, from, to)
					before := len(it.History)
					err := transition(&it, to, Actor{Kind: actor, By: "someone"}, "a reason", at(30))

					want, row := wantTransitions[[2]Status{from, to}]
					if row && want == actor {
						if err != nil {
							t.Fatalf("allowed transition refused: %v", err)
						}
						if it.Status != to || len(it.History) != before+1 {
							t.Fatalf("status %s with %d events, want %s with %d", it.Status, len(it.History), to, before+1)
						}
						e := it.History[len(it.History)-1]
						if e.From != from || e.To != to || e.Actor != actor || e.By != "someone" || e.Reason != "a reason" || !e.At.Equal(at(30)) {
							t.Errorf("event = %+v", e)
						}
						return
					}
					if !errors.Is(err, ErrTransitionNotAllowed) {
						t.Fatalf("err = %v, want ErrTransitionNotAllowed", err)
					}
					if len(it.History) != before {
						t.Error("a refused transition appended an event")
					}
				})
			}
		}
	}
}

func TestTransitionTableMatchesSpecification(t *testing.T) {
	if len(transitions) != len(wantTransitions) {
		t.Fatalf("%d rows, want %d", len(transitions), len(wantTransitions))
	}
	for k, actor := range transitions {
		if wantTransitions[[2]Status{k.from, k.to}] != actor {
			t.Errorf("row %s -> %s is made by %s", k.from, k.to, actor)
		}
	}
}

func TestTransitionConditions(t *testing.T) {
	human := Actor{Kind: ActorHuman, By: testHuman}
	tests := []struct {
		name    string
		item    func(t *testing.T) Item
		to      Status
		actor   Actor
		reason  string
		wantErr error
	}{
		{
			name:    "validate while a disagreement is unresolved",
			item:    func(t *testing.T) Item { return itemIn(t, StatusNeedsReview, true) },
			to:      StatusValidated,
			actor:   human,
			reason:  testReasonOK,
			wantErr: ErrUnresolvedDisagreement,
		},
		{
			name:    "contested to proposed while a disagreement is unresolved",
			item:    func(t *testing.T) Item { return itemIn(t, StatusContested, true) },
			to:      StatusProposed,
			actor:   human,
			reason:  ReasonDisagreementsResolved,
			wantErr: ErrUnresolvedDisagreement,
		},
		{
			name:    "reopen as proposed while a disagreement is unresolved",
			item:    func(t *testing.T) Item { return itemIn(t, StatusRejected, true) },
			to:      StatusProposed,
			actor:   human,
			reason:  "reconsidered",
			wantErr: ErrUnresolvedDisagreement,
		},
		{
			name:    "reopen as contested without an unresolved disagreement",
			item:    func(t *testing.T) Item { return itemIn(t, StatusRejected, false) },
			to:      StatusContested,
			actor:   human,
			reason:  "reconsidered",
			wantErr: ErrTransitionNotAllowed,
		},
		{
			name:    "contested without an unresolved disagreement",
			item:    func(t *testing.T) Item { return itemIn(t, StatusProposed, false) },
			to:      StatusContested,
			actor:   Actor{Kind: ActorRun, By: testRun2},
			reason:  "conflict",
			wantErr: ErrTransitionNotAllowed,
		},
		{
			name:    "human change without a reason",
			item:    func(t *testing.T) Item { return itemIn(t, StatusProposed, false) },
			to:      StatusValidated,
			actor:   human,
			reason:  "  ",
			wantErr: ErrReasonRequired,
		},
		{
			name:    "change without an author",
			item:    func(t *testing.T) Item { return itemIn(t, StatusProposed, false) },
			to:      StatusValidated,
			actor:   Actor{Kind: ActorHuman},
			reason:  testReasonOK,
			wantErr: ErrActorRequired,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := tt.item(t)
			before := it.clone()
			err := transition(&it, tt.to, tt.actor, tt.reason, at(30))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			unchanged(t, before, it)
		})
	}
}

func TestTransitionTime(t *testing.T) {
	human := Actor{Kind: ActorHuman, By: testHuman}
	tests := []struct {
		name string
		at   time.Time
	}{
		{name: "zero time", at: time.Time{}},
		{name: "time not in UTC", at: at(30).In(time.FixedZone("CET", 3600))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := itemIn(t, StatusProposed, false)
			if err := transition(&it, StatusValidated, human, testReasonOK, tt.at); !errors.Is(err, ErrTimeRequired) {
				t.Errorf("err = %v, want ErrTimeRequired", err)
			}
		})
	}
}
