package app

import (
	"testing"
	"time"
)

func TestValidateInterval(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, Moscow)
	for _, tt := range []struct {
		name, start, end string
		valid            bool
	}{
		{"minimum", "2026-01-02T09:00:00+03:00", "2026-01-02T09:30:00+03:00", true},
		{"full day", "2026-01-02T09:00:00+03:00", "2026-01-02T21:00:00+03:00", true},
		{"quarter hour", "2026-01-02T09:15:00+03:00", "2026-01-02T10:45:00+03:00", true},
		{"UTC equivalent", "2026-01-02T06:00:00Z", "2026-01-02T18:00:00Z", true},
		{"too short", "2026-01-02T09:00:00+03:00", "2026-01-02T09:15:00+03:00", false},
		{"wrong step", "2026-01-02T09:10:00+03:00", "2026-01-02T10:10:00+03:00", false},
		{"seconds", "2026-01-02T09:00:01+03:00", "2026-01-02T10:00:01+03:00", false},
		{"fraction", "2026-01-02T09:00:00.001+03:00", "2026-01-02T10:00:00.001+03:00", false},
		{"before opening", "2026-01-02T08:45:00+03:00", "2026-01-02T09:30:00+03:00", false},
		{"after close", "2026-01-02T20:45:00+03:00", "2026-01-02T21:15:00+03:00", false},
		{"overnight", "2026-01-02T20:00:00+03:00", "2026-01-03T10:00:00+03:00", false},
		{"past", "2025-01-02T09:00:00+03:00", "2025-01-02T10:00:00+03:00", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := time.Parse(time.RFC3339Nano, tt.start)
			e, _ := time.Parse(time.RFC3339Nano, tt.end)
			err := ValidateInterval(s, e, now)
			if (err == nil) != tt.valid {
				t.Fatalf("unexpected validation result: %v", err)
			}
		})
	}
}
