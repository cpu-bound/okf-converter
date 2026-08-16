package bundle

import (
	"fmt"
	"time"
)

// Entry is one recorded step of a conversion, in the order it happened.
type Entry struct {
	At      time.Time
	Message string
}

// Log accumulates the trace that ends up in the bundle's log.md: what the
// pipeline did, in what order, and with what outcome. It's threaded through
// the conversion rather than assembled at the end, so log.md reflects the
// run that actually happened - including the steps that failed - instead of
// a summary written after the fact.
//
// A Log is not safe for concurrent use: one conversion, one Log, one
// goroutine.
type Log struct {
	entries []Entry
	now     func() time.Time
}

func NewLog() *Log {
	return &Log{now: time.Now}
}

// newLogAt builds a Log with a fixed clock, so tests can assert on rendered
// timestamps.
func newLogAt(clock func() time.Time) *Log {
	return &Log{now: clock}
}

// Step records one operation. Callers phrase messages as completed actions
// ("3 unidades detectadas"), since that is how they read in log.md.
func (l *Log) Step(format string, args ...any) {
	l.entries = append(l.entries, Entry{At: l.now().UTC(), Message: fmt.Sprintf(format, args...)})
}

func (l *Log) Entries() []Entry {
	return l.entries
}
