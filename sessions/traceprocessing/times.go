package traceprocessing

import "time"

type TimeBounds struct {
	Start *time.Time
	End   *time.Time
}

func (b *TimeBounds) Observe(value *time.Time) {
	if value == nil {
		return
	}
	at := *value
	if b.Start == nil || at.Before(*b.Start) {
		start := at
		b.Start = &start
	}
	if b.End == nil || at.After(*b.End) {
		end := at
		b.End = &end
	}
}
