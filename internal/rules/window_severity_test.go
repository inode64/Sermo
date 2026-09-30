package rules

import (
	"math/rand/v2"
	"testing"
	"time"

	"sermo/internal/severity"
)

// gradedSample is one cycle of a graded owner: the condition, its grade, and
// the step the owner must observe.
type gradedSample struct {
	cond   bool
	grade  severity.Level
	firing bool
	level  severity.Level
	raised bool
}

func runGraded(t *testing.T, r Rule, samples []gradedSample) *WindowState {
	t.Helper()
	s := &WindowState{}
	at := time.Unix(1_700_000_000, 0)
	for i, sample := range samples {
		got := s.FiresGradedAt(r, sample.cond, sample.grade, at)
		want := EpisodeStep{Firing: sample.firing, Severity: sample.level, Raised: sample.raised}
		if got != want {
			t.Fatalf("cycle %d (%v %s): step = %+v, want %+v", i+1, sample.cond, sample.grade, got, want)
		}
		at = at.Add(time.Minute)
	}
	return s
}

func TestGradedEpisodeEscalatesOnlyAfterItsWindow(t *testing.T) {
	r := Rule{For: &ForWindow{Cycles: 2}}
	runGraded(t, r, []gradedSample{
		{cond: true, grade: severity.Warning},
		{cond: true, grade: severity.Warning, firing: true, level: severity.Warning, raised: true},
		// One error cycle is not a sustained error.
		{cond: true, grade: severity.Error, firing: true, level: severity.Warning},
		{cond: true, grade: severity.Error, firing: true, level: severity.Error, raised: true},
		// Easing off is held, not announced.
		{cond: true, grade: severity.Warning, firing: true, level: severity.Error},
		{cond: true, grade: severity.Warning, firing: true, level: severity.Error},
		// A critical spike must hold for the window too.
		{cond: true, grade: severity.Critical, firing: true, level: severity.Error},
		{cond: true, grade: severity.Warning, firing: true, level: severity.Error},
		// Recovery reports the mark the episode closed with.
		{cond: false, firing: false, level: severity.Error},
		// A new episode starts from its own grade.
		{cond: true, grade: severity.Warning},
		{cond: true, grade: severity.Warning, firing: true, level: severity.Warning, raised: true},
	})
}

func TestGradedEpisodeOpensAtItsSustainedLevel(t *testing.T) {
	// The first cycle opens the episode with no window: an error at once.
	runGraded(t, Rule{}, []gradedSample{
		{cond: true, grade: severity.Error, firing: true, level: severity.Error, raised: true},
		{cond: true, grade: severity.Critical, firing: true, level: severity.Critical, raised: true},
		{cond: true, grade: severity.Error, firing: true, level: severity.Critical},
	})
	// Mixed grades while the window matures open at the level held throughout.
	runGraded(t, Rule{For: &ForWindow{Cycles: 3}}, []gradedSample{
		{cond: true, grade: severity.Critical},
		{cond: true, grade: severity.Warning},
		{cond: true, grade: severity.Critical, firing: true, level: severity.Warning, raised: true},
	})
}

func TestGradedEpisodeClearWindowHoldsItsMark(t *testing.T) {
	r := Rule{Clear: &ForWindow{Cycles: 2}}
	runGraded(t, r, []gradedSample{
		{cond: true, grade: severity.Critical, firing: true, level: severity.Critical, raised: true},
		// A dip inside the clear window neither resets nor re-announces.
		{cond: false, firing: true, level: severity.Critical},
		{cond: true, grade: severity.Warning, firing: true, level: severity.Critical},
		{cond: false, firing: true, level: severity.Critical},
		{cond: false, firing: false, level: severity.Critical},
	})
}

func TestGradedEpisodeDurationWindow(t *testing.T) {
	r := Rule{For: &ForWindow{Duration: 2 * time.Minute}}
	runGraded(t, r, []gradedSample{
		{cond: true, grade: severity.Warning},
		{cond: true, grade: severity.Error},
		{cond: true, grade: severity.Error, firing: true, level: severity.Warning, raised: true},
		{cond: true, grade: severity.Error, firing: true, level: severity.Error, raised: true},
	})
}

func TestGradedEpisodeWithinWindow(t *testing.T) {
	r := Rule{Within: &WithinWindow{Cycles: 3, MinMatches: 2}}
	runGraded(t, r, []gradedSample{
		{cond: true, grade: severity.Error},
		{cond: false},
		{cond: true, grade: severity.Warning, firing: true, level: severity.Warning, raised: true},
		// The first error slid out of the window: one error in the last three.
		{cond: true, grade: severity.Error, firing: true, level: severity.Warning},
		{cond: true, grade: severity.Error, firing: true, level: severity.Error, raised: true},
	})
}

func TestGradedEpisodeNotifiedResetsWithTheEpisode(t *testing.T) {
	s := &WindowState{}
	at := time.Now()
	s.MarkNotified(severity.Warning)
	if s.Notified().Valid() {
		t.Fatal("a closed episode recorded a notification")
	}
	s.FiresGradedAt(Rule{}, true, severity.Error, at)
	s.MarkNotified(severity.Warning)
	s.MarkNotified(severity.Error)
	s.MarkNotified(severity.Info) // never lowers the mark
	if s.Notified() != severity.Error || s.Severity() != severity.Error {
		t.Fatalf("open episode notified=%q severity=%q", s.Notified(), s.Severity())
	}
	s.FiresGradedAt(Rule{}, false, "", at)
	if s.Notified().Valid() || s.Severity().Valid() {
		t.Fatalf("after recovery notified=%q severity=%q, want both reset", s.Notified(), s.Severity())
	}
	s.FiresGradedAt(Rule{}, true, severity.Warning, at)
	s.MarkNotified(severity.Warning)
	s.EndEpisode()
	if s.Notified().Valid() || s.Severity().Valid() {
		t.Fatal("EndEpisode kept the episode's grading")
	}
}

// A state restored from before grading existed carries an open episode with
// no severity and no rungs: it adopts the current grade without announcing
// the episode again.
func TestGradedEpisodeSeedsALegacyRecordSilently(t *testing.T) {
	s := WindowStateFromSnapshot(WindowStateSnapshot{Consecutive: 3, Firing: true})
	r := Rule{For: &ForWindow{Cycles: 3}}
	step := s.FiresGradedAt(r, true, severity.Warning, time.Now())
	if step != (EpisodeStep{Firing: true, Severity: severity.Warning}) {
		t.Fatalf("legacy seed step = %+v, want a silent warning", step)
	}
	// The rungs were seeded from the entry window, so a warning history does
	// not escalate early to an error.
	if step := s.FiresGradedAt(r, true, severity.Error, time.Now()); step.Raised {
		t.Fatalf("one error cycle escalated a seeded episode: %+v", step)
	}
}

func TestGradedSnapshotRoundTrip(t *testing.T) {
	r := Rule{For: &ForWindow{Cycles: 2}}
	at := time.Unix(1_700_000_000, 0)
	s := &WindowState{}
	s.FiresGradedAt(r, true, severity.Warning, at)
	s.FiresGradedAt(r, true, severity.Warning, at)
	s.FiresGradedAt(r, true, severity.Error, at)
	s.MarkNotified(severity.Warning)
	restored := WindowStateFromSnapshot(s.Snapshot())
	if restored.Severity() != severity.Warning || restored.Notified() != severity.Warning {
		t.Fatalf("restored severity=%q notified=%q", restored.Severity(), restored.Notified())
	}
	// The error rung's progress survived: one more error cycle escalates.
	if step := restored.FiresGradedAt(r, true, severity.Error, at); !step.Raised || step.Severity != severity.Error {
		t.Fatalf("restored step = %+v, want an escalation to error", step)
	}
	clone := restored.Clone()
	clone.FiresGradedAt(r, false, "", at)
	if restored.Severity() != severity.Error {
		t.Fatal("Clone shares grading state with its source")
	}
	if got := WindowStateFromSnapshot(WindowStateSnapshot{Rungs: []EntryWindowSnapshot{{}}}); got.rungs != nil {
		t.Fatal("a snapshot with the wrong rung count was restored")
	}
}

// Grading never changes the episode itself: FiresGradedAt fires exactly when
// FiresAt would for the same condition sequence.
func TestFiresGradedAtMatchesFiresAt(t *testing.T) {
	rules := []Rule{
		{},
		{For: &ForWindow{Cycles: 3}},
		{For: &ForWindow{Duration: 3 * time.Minute}},
		{Within: &WithinWindow{Cycles: 4, MinMatches: 2}},
		{For: &ForWindow{Cycles: 2}, Clear: &ForWindow{Cycles: 3}},
	}
	rng := rand.New(rand.NewPCG(1, 2))
	levels := []severity.Level{severity.Debug, severity.Info, severity.Warning, severity.Error, severity.Critical}
	for _, r := range rules {
		plain, graded := &WindowState{}, &WindowState{}
		at := time.Unix(1_700_000_000, 0)
		for i := range 500 {
			cond := rng.IntN(3) > 0
			level := levels[rng.IntN(len(levels))]
			want := plain.FiresAt(r, cond, at)
			if got := graded.FiresGradedAt(r, cond, level, at); got.Firing != want {
				t.Fatalf("rule %+v cycle %d: graded firing %v, FiresAt %v", r, i, got.Firing, want)
			}
			at = at.Add(time.Minute)
		}
	}
}
