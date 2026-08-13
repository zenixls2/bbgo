package gammacapture

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestShouldLogBOCPD45Status(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	require.True(t, shouldLogBOCPD45Status(time.Time{}, now), "the first live observation must be visible")
	require.False(t, shouldLogBOCPD45Status(now, now.Add(59*time.Second)), "sub-minute BBO traffic must not flood logs")
	require.True(t, shouldLogBOCPD45Status(now, now.Add(time.Minute)), "the status must recur once per minute")
	require.True(t, shouldLogBOCPD45Status(now, now.Add(-time.Second)), "a wall-clock correction must not silence diagnostics")
}
