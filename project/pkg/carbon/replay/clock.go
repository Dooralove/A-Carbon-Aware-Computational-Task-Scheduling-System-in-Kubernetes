package replay

import (
	"sync"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

// ScaledClock maps wall elapsed time onto model time with a multiplier.
// virtual = originModel + (wallNow - originWall) * multiplier, truncated to 1 ms.
type ScaledClock struct {
	mu          sync.Mutex
	wall        v1alpha1.Clock
	originWall  time.Time
	originModel time.Time
	multiplier  float64
}

// NewScaledClock starts a virtual clock at originModel. multiplier <= 0 is treated as 1.
func NewScaledClock(wall v1alpha1.Clock, originModel time.Time, multiplier float64) *ScaledClock {
	if wall == nil {
		wall = v1alpha1.SystemClock{}
	}
	if multiplier <= 0 {
		multiplier = 1
	}
	now := wall.Now().UTC()
	return &ScaledClock{
		wall:        wall,
		originWall:  now,
		originModel: originModel.UTC(),
		multiplier:  multiplier,
	}
}

// Now implements v1alpha1.Clock with millisecond resolution.
func (c *ScaledClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	elapsed := c.wall.Now().UTC().Sub(c.originWall)
	scaled := time.Duration(float64(elapsed) * c.multiplier)
	return c.originModel.Add(scaled).UTC().Truncate(time.Millisecond)
}

// Multiplier returns the configured time acceleration factor.
func (c *ScaledClock) Multiplier() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.multiplier
}

var _ v1alpha1.Clock = (*ScaledClock)(nil)
