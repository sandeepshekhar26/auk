package license

import (
	"testing"
	"time"
)

func TestGraceState(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	grace := time.Duration(GraceDays) * 24 * time.Hour

	t.Run("zero lastValidated is treated as in grace", func(t *testing.T) {
		// Never phoned home yet — must NOT be treated as lapsed (offline-first).
		in, days := graceState(time.Time{}, now, grace)
		if !in {
			t.Error("zero lastValidated should be in grace")
		}
		if days != GraceDays {
			t.Errorf("days = %d, want %d", days, GraceDays)
		}
	})

	t.Run("recently validated is in grace with days remaining", func(t *testing.T) {
		in, days := graceState(now.Add(-10*24*time.Hour), now, grace)
		if !in {
			t.Error("validated 10 days ago should still be in grace")
		}
		if days != GraceDays-10 {
			t.Errorf("days = %d, want %d", days, GraceDays-10)
		}
	})

	t.Run("exactly at the deadline is lapsed", func(t *testing.T) {
		in, days := graceState(now.Add(-grace), now, grace)
		if in {
			t.Error("at the deadline grace should be lapsed")
		}
		if days != 0 {
			t.Errorf("days = %d, want 0", days)
		}
	})

	t.Run("past the deadline is lapsed", func(t *testing.T) {
		in, days := graceState(now.Add(-grace-24*time.Hour), now, grace)
		if in {
			t.Error("past the deadline grace should be lapsed")
		}
		if days != 0 {
			t.Errorf("days = %d, want 0", days)
		}
	})

	t.Run("any time left rounds up to at least one day", func(t *testing.T) {
		in, days := graceState(now.Add(-grace+time.Hour), now, grace) // 1h of grace left
		if !in {
			t.Error("with an hour of grace left it should still be in grace")
		}
		if days != 1 {
			t.Errorf("days = %d, want 1", days)
		}
	})
}
