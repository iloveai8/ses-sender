package campaign

import (
	"testing"
	"time"
)

// 表驱动全量边界用例（=docs/v2/06-ENGINE-DESIGN.md §8 的 11 用例表）
func TestCalcNextRun(t *testing.T) {
	utc := func(s string) time.Time {
		tt, err := time.Parse("2006-01-02T15:04:05", s)
		if err != nil {
			t.Fatal(err)
		}
		return tt
	}
	ptr := func(i int) *int { return &i }

	cases := []struct {
		name     string
		typ      string
		sched    time.Time
		hour     int
		minute   int
		dow, dom *int
		after    time.Time
		want     string // "" = nil
	}{
		{"once 未来", "once", utc("2027-06-01T09:00:00"), 0, 0, nil, nil, utc("2026-09-16T00:00:00"), "2027-06-01T09:00:00"},
		{"once 过去", "once", utc("2020-01-01T09:00:00"), 0, 0, nil, nil, utc("2026-09-16T00:00:00"), ""},
		{"daily 未到点", "daily", time.Time{}, 9, 0, nil, nil, utc("2026-09-16T08:00:00"), "2026-09-16T09:00:00"},
		{"daily 已过点", "daily", time.Time{}, 9, 0, nil, nil, utc("2026-09-16T10:00:00"), "2026-09-17T09:00:00"},
		{"weekly 未来天数", "weekly", time.Time{}, 9, 0, ptr(3), nil, utc("2026-09-16T08:00:00"), "2026-09-17T09:00:00"}, // 周三→周四(3)
		{"weekly 已过+7", "weekly", time.Time{}, 9, 0, ptr(0), nil, utc("2026-09-16T10:00:00"), "2026-09-21T09:00:00"}, // 周三→下周一
		{"weekly 未指定dow按周一", "weekly", time.Time{}, 9, 0, nil, nil, utc("2026-09-16T10:00:00"), "2026-09-21T09:00:00"},
		{"monthly 正常未过", "monthly", time.Time{}, 9, 0, nil, ptr(15), utc("2026-09-10T10:00:00"), "2026-09-15T09:00:00"},
		{"monthly 已过进次月", "monthly", time.Time{}, 9, 0, nil, ptr(15), utc("2026-09-16T10:00:00"), "2026-10-15T09:00:00"},
		{"monthly 超月末clamp", "monthly", time.Time{}, 9, 0, nil, ptr(31), utc("2026-02-10T10:00:00"), "2026-02-28T09:00:00"},
		{"monthly dom=0按1号", "monthly", time.Time{}, 9, 0, nil, ptr(0), utc("2026-09-16T10:00:00"), "2026-10-01T09:00:00"},
		{"monthly 已过进月", "monthly", time.Time{}, 9, 0, nil, ptr(1), utc("2026-12-15T10:00:00"), "2027-01-01T09:00:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CalcNextRun(c.typ, c.sched, c.hour, c.minute, c.dow, c.dom, c.after)
			if c.want == "" {
				if got != nil {
					t.Fatalf("期望 nil，实际 %v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("期望 %s，实际 nil", c.want)
			}
			if got.UTC().Format("2006-01-02T15:04:05") != c.want {
				t.Fatalf("期望 %s，实际 %s", c.want, got.UTC().Format("2006-01-02T15:04:05"))
			}
		})
	}
}
