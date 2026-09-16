package campaign

import "time"

// CalcNextRun 下次执行时间计算（=Python _calc_next_run 全量移植；全 UTC naive；返回 nil=不再执行）。
// 边界用例表见 docs/v2/06-ENGINE-DESIGN.md §8 与 next_run_test.go。
// 注意 weekday 口径：Python weekday() 周一=0；Go Weekday() 周日=0 → (wd+6)%7 转换。
func CalcNextRun(scheduleType string, scheduledTime time.Time, cronHour, cronMinute int,
	dayOfWeek, dayOfMonth *int, after time.Time) *time.Time {

	after = after.UTC()
	switch scheduleType {
	case "once":
		if scheduledTime.After(after) {
			t := scheduledTime.UTC()
			return &t
		}
		return nil

	case "daily":
		base := time.Date(after.Year(), after.Month(), after.Day(), cronHour, cronMinute, 0, 0, time.UTC)
		if base.After(after) {
			return &base
		}
		t := base.Add(24 * time.Hour)
		return &t

	case "weekly":
		dow := 0
		if dayOfWeek != nil {
			dow = *dayOfWeek
		}
		pyToday := (int(after.Weekday()) + 6) % 7 // 周一=0 口径
		diff := (dow - pyToday + 7) % 7
		base := time.Date(after.Year(), after.Month(), after.Day(), cronHour, cronMinute, 0, 0, time.UTC).
			AddDate(0, 0, diff)
		if base.After(after) {
			return &base
		}
		t := base.AddDate(0, 0, 7)
		return &t

	case "monthly":
		dom := 1
		if dayOfMonth != nil && *dayOfMonth > 0 {
			dom = *dayOfMonth
		}
		day := clampToMonth(after.Year(), after.Month(), dom)
		nxt := time.Date(after.Year(), after.Month(), day, cronHour, cronMinute, 0, 0, time.UTC)
		if nxt.After(after) {
			return &nxt
		}
		y, m := after.Year(), int(after.Month())+1 // 12 月→次年 1 月（AddDate 自动进位）
		nxt2 := time.Date(y, time.Month(m), 1, cronHour, cronMinute, 0, 0, time.UTC).AddDate(0, 0, clampToMonth(y, time.Month(m), dom)-1)
		return &nxt2
	}
	return nil
}

// clampToMonth 日截断到月末（2 月兜底 28——Python ValueError 分支）
func clampToMonth(y int, m time.Month, day int) int {
	if day > 28 { // 仅 29/30/31 需要截断；月末天数表
		max := 31
		switch m {
		case 4, 6, 9, 11:
			max = 30
		case 2:
			max = 28 // 兜底 28（Python 对 2 月异常统一落 28；闰年 29 也不越界）
			if isLeap(y) {
				max = 29
			}
		}
		if day > max {
			return max
		}
	}
	return day
}

func isLeap(y int) bool {
	return y%4 == 0 && (y%100 != 0 || y%400 == 0)
}
