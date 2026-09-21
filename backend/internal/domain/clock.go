package domain

import "time"

// Clock supplies the current time. Every transaction that stamps a persisted fact takes
// its time from a Clock so that deadline, backoff and timeout behaviour can be tested
// without sleeping.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the wall clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }
