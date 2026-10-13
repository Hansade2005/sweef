package store

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Budget is the Data Saver allowance (settings key data.budget). It is plain
// data plus pure functions of a clock, so the period and threshold maths can
// be tested with any time, including clock changes.
//
//	Enabled  - false means "Unlimited": usage is still counted.
//	LimitMB  - allowance per period in MB (1 MB = 1<<20 bytes).
//	Period   - "day" | "week" | "month".
//	ResetDay - week: weekday the period starts (0 = Sunday .. 6 = Saturday);
//	           month: day of month (1..28; larger values are clamped to the
//	           last day of short months); day: ignored.
//	WarnAt   - percentages at which the person is told once per period.
//	HardStop - when the budget is used up, page loads stop at an interstitial
//	           that offers "Load once", "Raise limit" and "Wait until reset".
type Budget struct {
	Enabled  bool   `json:"enabled"`
	LimitMB  int    `json:"limitMB"`
	Period   string `json:"period"`
	ResetDay int    `json:"resetDay"`
	WarnAt   []int  `json:"warnAt"`
	HardStop bool   `json:"hardStop"`
}

// MB is one megabyte as the budget counts it.
const MB = 1 << 20

// DefaultBudget is what an unconfigured profile has: no limit, month period.
func DefaultBudget() Budget {
	return Budget{Enabled: false, LimitMB: 100, Period: "month", ResetDay: 1, WarnAt: []int{70, 90}}
}

// ParseBudget turns a settings value (a map decoded from JSON, a Budget, or
// nil) into a validated Budget. Unknown or invalid fields fall back to the
// defaults; an error is returned only for a value that is not an object.
func ParseBudget(v any) (Budget, error) {
	b := DefaultBudget()
	switch x := v.(type) {
	case nil:
		return b, nil
	case Budget:
		b = x
	case map[string]any:
		if e, ok := x["enabled"].(bool); ok {
			b.Enabled = e
		}
		if n, ok := toInt(x["limitMB"]); ok {
			b.LimitMB = n
		}
		if s, ok := x["period"].(string); ok {
			b.Period = s
		}
		if n, ok := toInt(x["resetDay"]); ok {
			b.ResetDay = n
		}
		if h, ok := x["hardStop"].(bool); ok {
			b.HardStop = h
		}
		if w, ok := x["warnAt"].([]any); ok {
			b.WarnAt = nil
			for _, e := range w {
				if n, ok := toInt(e); ok {
					b.WarnAt = append(b.WarnAt, n)
				}
			}
		} else if w, ok := x["warnAt"].([]int); ok {
			b.WarnAt = append([]int(nil), w...)
		}
	default:
		return b, fmt.Errorf("data.budget: want an object")
	}
	return b.Normalized(), nil
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e9 {
			return int(x), true
		}
	case int:
		return x, true
	case int64:
		return int(x), true
	}
	return 0, false
}

// Normalized clamps every field into its valid range.
func (b Budget) Normalized() Budget {
	switch b.Period {
	case "day", "week", "month":
	default:
		b.Period = "month"
	}
	if b.LimitMB < 1 {
		b.LimitMB = 1
	}
	if b.LimitMB > 1<<20 { // 1 TB
		b.LimitMB = 1 << 20
	}
	switch b.Period {
	case "week":
		if b.ResetDay < 0 || b.ResetDay > 6 {
			b.ResetDay = 1 // Monday
		}
	case "month":
		if b.ResetDay < 1 {
			b.ResetDay = 1
		}
		if b.ResetDay > 28 {
			b.ResetDay = 28
		}
	default:
		b.ResetDay = 1
	}
	seen := map[int]bool{}
	var w []int
	for _, p := range b.WarnAt {
		if p >= 1 && p <= 99 && !seen[p] {
			seen[p] = true
			w = append(w, p)
		}
	}
	sort.Ints(w)
	if w == nil {
		w = []int{}
	}
	b.WarnAt = w
	return b
}

// Map is the budget as the settings JSON holds it.
func (b Budget) Map() map[string]any {
	w := make([]any, len(b.WarnAt))
	for i, p := range b.WarnAt {
		w[i] = p
	}
	return map[string]any{"enabled": b.Enabled, "limitMB": b.LimitMB, "period": b.Period,
		"resetDay": b.ResetDay, "warnAt": w, "hardStop": b.HardStop}
}

// LimitBytes is the allowance in bytes (0 when the budget is off).
func (b Budget) LimitBytes() int64 {
	if !b.Enabled {
		return 0
	}
	return int64(b.LimitMB) * MB
}

// DayKey is the local calendar date of t as used for the daily records.
func DayKey(t time.Time) string { return t.Format("2006-01-02") }

func midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func daysIn(y int, m time.Month, loc *time.Location) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, loc).Day()
}

// PeriodStart is the local midnight on which the period containing now began.
func (b Budget) PeriodStart(now time.Time) time.Time {
	b = b.Normalized()
	today := midnight(now)
	switch b.Period {
	case "day":
		return today
	case "week":
		back := (int(today.Weekday()) - b.ResetDay + 7) % 7
		return today.AddDate(0, 0, -back)
	}
	// month: the most recent reset date at or before today
	y, m, _ := today.Date()
	day := min(b.ResetDay, daysIn(y, m, today.Location()))
	start := time.Date(y, m, day, 0, 0, 0, 0, today.Location())
	if start.After(today) {
		py, pm := y, m-1
		if pm < 1 {
			py, pm = y-1, 12
		}
		day = min(b.ResetDay, daysIn(py, pm, today.Location()))
		start = time.Date(py, pm, day, 0, 0, 0, 0, today.Location())
	}
	return start
}

// PeriodEnd is the local midnight on which the next period starts.
func (b Budget) PeriodEnd(now time.Time) time.Time {
	b = b.Normalized()
	start := b.PeriodStart(now)
	switch b.Period {
	case "day":
		return start.AddDate(0, 0, 1)
	case "week":
		return start.AddDate(0, 0, 7)
	}
	y, m, _ := start.Date()
	ny, nm := y, m+1
	if nm > 12 {
		ny, nm = y+1, 1
	}
	day := min(b.ResetDay, daysIn(ny, nm, start.Location()))
	return time.Date(ny, nm, day, 0, 0, 0, 0, start.Location())
}

// PeriodKey identifies a period (its start date); thresholds fire once per key.
func (b Budget) PeriodKey(now time.Time) string {
	n := b.Normalized()
	return n.Period + ":" + DayKey(n.PeriodStart(now))
}

// ResetsInDays is the number of calendar days from today until the reset
// (1 = tomorrow). It is never below 1.
func (b Budget) ResetsInDays(now time.Time) int {
	end := b.PeriodEnd(now)
	today := midnight(now)
	// count by calendar date, not by 24 h blocks, so DST changes do not skew it
	n := 0
	for d := today; d.Before(end) && n < 400; d = d.AddDate(0, 0, 1) {
		n++
	}
	if n < 1 {
		n = 1
	}
	return n
}

// BudgetStatus is the budget maths for one moment.
type BudgetStatus struct {
	Enabled      bool      `json:"enabled"`
	LimitBytes   int64     `json:"limitBytes"`
	UsedBytes    int64     `json:"usedBytes"`
	LeftBytes    int64     `json:"leftBytes"`
	Pct          float64   `json:"pct"` // 0..100+, 0 when no budget
	ResetsInDays int       `json:"resetsInDays"`
	ResetsAt     time.Time `json:"resetsAt"`
	PeriodStart  time.Time `json:"periodStart"`
	Exhausted    bool      `json:"exhausted"`
	HardStop     bool      `json:"hardStop"`
	Key          string    `json:"key"`
	WarnCrossed  []int     `json:"-"` // thresholds reached (not necessarily announced)
}

// StatusFor computes the status for a given usage in the current period.
func (b Budget) StatusFor(used int64, now time.Time) BudgetStatus {
	b = b.Normalized()
	s := BudgetStatus{Enabled: b.Enabled, UsedBytes: used, ResetsInDays: b.ResetsInDays(now),
		ResetsAt: b.PeriodEnd(now), PeriodStart: b.PeriodStart(now), HardStop: b.HardStop, Key: b.PeriodKey(now)}
	if !b.Enabled {
		return s
	}
	s.LimitBytes = b.LimitBytes()
	s.LeftBytes = max(s.LimitBytes-used, 0)
	s.Pct = float64(used) * 100 / float64(s.LimitBytes)
	s.Exhausted = used >= s.LimitBytes
	for _, w := range b.WarnAt {
		if s.Pct >= float64(w) {
			s.WarnCrossed = append(s.WarnCrossed, w)
		}
	}
	return s
}
