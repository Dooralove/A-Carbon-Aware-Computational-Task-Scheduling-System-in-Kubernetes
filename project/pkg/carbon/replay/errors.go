// Package replay is the Carbon Data Service Replay Engine (ARCH-12, REPRO-03).
//
// Model time is independent of wall-clock. GET-style queries never advance the
// clock; a ScaledClock (time multiplier) or FakeClock is injected by the caller.
package replay

import "errors"

var (
	// ErrInvalidDataset is returned when a Replay JSON/CSV file fails schema or G-10 checks.
	ErrInvalidDataset = errors.New("invalid replay dataset")

	// ErrNoSample means the mapped model time has no covering series interval.
	ErrNoSample = errors.New("no carbon sample for model time")
)
