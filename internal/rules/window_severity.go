package rules

import (
	"slices"
	"time"

	"sermo/internal/severity"
)

// Escalate and hold. A graded owner — a watch or rule whose true condition
// carries a severity — escalates its open episode only when a graver level has
// held for the owner's own entry window, the same for:/within: that opened the
// episode. The episode keeps the gravest level it reached until it recovers: a
// level that eases off is not announced, so a value oscillating around a
// threshold costs at most one message per level per episode. The recovery
// reports that high-water mark, so whoever was told of the incident is told it
// ended.

// gradedRungs are the levels a rung tracks, ascending. Debug needs none: every
// true condition is at least debug, so the episode's own entry window is its
// rung.
var gradedRungs = [...]severity.Level{severity.Info, severity.Warning, severity.Error, severity.Critical}

// grading is the escalation state a WindowState carries for a graded owner.
type grading struct {
	// rungs holds one entry window per gradedRungs level, fed with "true and
	// graded at least this level". Nil until the owner grades its first sample.
	rungs *[len(gradedRungs)]entryWindow
	// severity is the open episode's high-water mark; unset outside one.
	severity severity.Level
	// notified is the gravest level the open episode delivered a notification
	// at; unset when it notified no one.
	notified severity.Level
}

// EpisodeStep is one graded cycle's outcome.
type EpisodeStep struct {
	// Firing reports whether the episode is open after this cycle.
	Firing bool
	// Severity is the episode's high-water mark. On the cycle the episode ends
	// it is the mark the episode closed with, so the recovery can report it.
	Severity severity.Level
	// Raised reports that this cycle opened the episode or escalated it.
	Raised bool
}

// FiresGradedAt is FiresAt for an owner whose true condition carries a grade.
// graded is ignored while the condition is false. It returns the same firing
// verdict FiresAt would, plus the episode's sustained severity.
func (s *WindowState) FiresGradedAt(r Rule, conditionTrue bool, graded severity.Level, at time.Time) EpisodeStep {
	graded = graded.Resolved()
	wasFiring := s.firing
	s.seedRungs(conditionTrue, graded)
	raw := s.advance(r, conditionTrue, at)
	sustained := severity.Debug
	for i := range s.rungs {
		if s.rungs[i].advance(r, conditionTrue && graded.Rank() >= gradedRungs[i].Rank(), at) {
			sustained = gradedRungs[i]
		}
	}
	firing := s.settle(r, raw, conditionTrue, at)
	switch {
	case !firing:
		closed := s.severity
		s.endGrading()
		return EpisodeStep{Severity: closed}
	case !wasFiring:
		s.severity = sustained
		return EpisodeStep{Firing: true, Severity: sustained, Raised: true}
	case conditionTrue && raw && sustained.Rank() > s.severity.Rank():
		s.severity = sustained
		return EpisodeStep{Firing: true, Severity: sustained, Raised: true}
	}
	return EpisodeStep{Firing: true, Severity: s.severity}
}

// seedRungs creates the rungs on an owner's first graded sample. A state
// restored from before grading existed has entry-window progress but no rungs:
// it is read as having held the current grade all along, which is the best
// available estimate and never announces an open episode twice. A restored
// open episode with no recorded severity adopts the current grade silently —
// only from a true condition: a false one (the episode held open by its clear
// window) carries no grade worth adopting.
func (s *WindowState) seedRungs(conditionTrue bool, graded severity.Level) {
	if s.firing && !s.severity.Valid() && conditionTrue {
		s.severity = graded
	}
	if s.rungs != nil {
		return
	}
	s.rungs = new([len(gradedRungs)]entryWindow)
	for i, level := range gradedRungs {
		if graded.Rank() >= level.Rank() {
			s.rungs[i] = s.entryWindow.clone()
		}
	}
}

// Severity reports the open episode's high-water mark (nil-safe); unset
// outside an episode or for an ungraded owner.
func (s *WindowState) Severity() severity.Level {
	if s == nil || !s.firing {
		return ""
	}
	return s.severity
}

// MarkNotified records that the open episode delivered a notification at
// level, so its recovery reaches exactly the notifiers that heard it.
func (s *WindowState) MarkNotified(level severity.Level) {
	if s != nil && s.firing {
		s.notified = severity.Max(s.notified, level.Resolved())
	}
}

// Notified is the gravest level the open episode delivered a notification
// at, or unset when it notified no one (nil-safe).
func (s *WindowState) Notified() severity.Level {
	if s == nil || !s.firing {
		return ""
	}
	return s.notified
}

func (g *grading) endGrading() {
	g.severity = ""
	g.notified = ""
}

func (g *grading) clone() grading {
	out := *g
	if g.rungs != nil {
		rungs := *g.rungs
		for i := range rungs {
			rungs[i] = rungs[i].clone()
		}
		out.rungs = &rungs
	}
	return out
}

func (g *grading) rungSnapshots() []EntryWindowSnapshot {
	if g.rungs == nil {
		return nil
	}
	out := make([]EntryWindowSnapshot, len(g.rungs))
	for i, rung := range g.rungs {
		out[i] = EntryWindowSnapshot{
			Consecutive:  rung.consecutive,
			History:      slices.Clone(rung.history),
			TrueSince:    rung.trueSince,
			TimedHistory: slices.Clone(rung.timedHistory),
		}
	}
	return out
}

// restoreRungs rebuilds persisted rungs; a snapshot of another length (a
// future or corrupt record) is dropped and the rungs re-seed on the next
// graded sample.
func (g *grading) restoreRungs(snapshots []EntryWindowSnapshot) {
	if len(snapshots) != len(gradedRungs) {
		return
	}
	g.rungs = new([len(gradedRungs)]entryWindow)
	for i, snap := range snapshots {
		g.rungs[i] = entryWindow{
			consecutive:  max(snap.Consecutive, 0),
			history:      slices.Clone(snap.History),
			trueSince:    snap.TrueSince,
			timedHistory: slices.Clone(snap.TimedHistory),
		}
	}
}
