package ephemeral

import (
	"fmt"
	"time"
)

// Facts are the observations the done rule judges (spec §2.2). Gathering
// them touches git, gh and the filesystem (facts.go); judging them touches
// nothing, so every combination is table-testable.
type Facts struct {
	OwnCommits int
	// PRState is "OPEN", "MERGED", "CLOSED", or "" when there is no PR or
	// its state could not be read. Unknown is never treated as merged.
	PRState  string
	LastUsed time.Time
	// OpenedAt is the later of the marker's created_at and last_opened_at.
	OpenedAt time.Time
	// Grace protects a worktree opened less than this long ago. Sweeps set
	// it; the SessionEnd hook and `wt reap <path>` leave it 0.
	Grace    time.Duration
	IdleDays int
	Now      time.Time
}

// Verdict is the done rule's answer and the reason a person would read in
// reap.log or `wt reap --dry-run`.
type Verdict struct {
	Done   bool
	Reason string
}

// Decide applies spec §2.2. It is only consulted after every safety check
// has passed; it decides "is this finished", never "is this safe".
func Decide(f Facts) Verdict {
	// A zero OpenedAt would make the grace check pass trivially and, with
	// no newer sign of use, read as idle since year 1: fail open twice over.
	if f.OpenedAt.IsZero() {
		return Verdict{Reason: "marker has no created_at"}
	}
	if f.Grace > 0 {
		if age := f.Now.Sub(f.OpenedAt); age < f.Grace {
			return Verdict{Reason: fmt.Sprintf("opened %s ago (sweep grace %s)", age.Round(time.Second), f.Grace)}
		}
	}
	if f.OwnCommits == 0 {
		return Verdict{Done: true, Reason: "no commits of your own"}
	}
	switch f.PRState {
	case "MERGED":
		return Verdict{Done: true, Reason: "PR merged"}
	case "CLOSED":
		return Verdict{Done: true, Reason: "PR closed"}
	}
	idle := f.Now.Sub(f.LastUsed)
	if idle >= time.Duration(f.IdleDays)*24*time.Hour {
		return Verdict{Done: true, Reason: fmt.Sprintf("idle %s (limit %dd)", days(idle), f.IdleDays)}
	}
	pr := "no PR"
	if f.PRState == "OPEN" {
		pr = "PR open"
	}
	return Verdict{Reason: fmt.Sprintf("%d commit(s) of your own; %s; idle %s of %dd",
		f.OwnCommits, pr, days(idle), f.IdleDays)}
}

func days(d time.Duration) string { return fmt.Sprintf("%.1fd", d.Hours()/24) }
