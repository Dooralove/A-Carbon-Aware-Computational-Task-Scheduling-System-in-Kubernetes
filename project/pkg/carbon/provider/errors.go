package provider

import (
	"errors"
	"net"
)

var (
	// ErrNotImplemented is retained for T4.1 stubs that have not been replaced.
	ErrNotImplemented = errors.New("carbon provider stub is not implemented")

	// ErrTimeout is returned when a live HTTP call exceeds its deadline (GO-09).
	ErrTimeout = errors.New("carbon provider request timed out")

	// ErrInvalidResponse is returned when the upstream body cannot be mapped to a CarbonRecord.
	ErrInvalidResponse = errors.New("carbon provider response is invalid")

	// ErrMissingAPIKey is returned when live mode is selected without a token.
	ErrMissingAPIKey = errors.New("live API key is required")
)

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrTimeout) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
