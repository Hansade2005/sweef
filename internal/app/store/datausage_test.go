package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func at(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 30, 0, 0, time.UTC) }

func TestBudgetPeriods(t *testing.T) {
	cases := []struct {
		name       string
		b          Budget
		now        time.Time
		start, end time.Time
		days       int
	}{
		{"day", Budget{Period: "day"}, at(2026, 3, 10, 15), at(2026, 3, 10, 0).Truncate(24 * time.Hour), at(2026, 3, 11, 0).Truncate(24 * time.Hour), 1},
		{"month reset 1", Budget{Period: "month", ResetDay: 1}, at(2026, 3, 10, 9), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), 22},
		{"month reset 15 before", Budget{Period: "month", ResetDay: 15}, at(2026, 3, 10, 9), time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), 5},
		{"month reset 15 on the day", Budget{Period: "month", ResetDay: 15}, at(2026, 3, 15, 0), time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC), 31},
		{"month reset 15 after, year end", Budget{Period: "month", ResetDay: 15}, at(2026, 12, 20, 9), time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC), 26},
		{"month in january uses december", Budget{Period: "month", ResetDay: 20}, at(2027, 1, 5, 9), time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC), 15},
		{"month reset 31 clamps to 28", Budget{Period: "month", ResetDay: 31}, at(2026, 2, 27, 9), time.Date(2026, 1, 28, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), 1},
		{"week resets monday", Budget{Period: "week", ResetDay: 1}, at(2026, 3, 11, 9), time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC), 5},
		{"week resets sunday on a sunday", Budget{Period: "week", ResetDay: 0}, at(2026, 3, 8, 9), time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), 7},
	}
	for _, c := range cases {
		c.b.Enabled, c.b.LimitMB = true, 100
		if got := c.b.PeriodStart(c.now); !got.Equal(c.start) {
			t.Errorf("%s: start %v want %v", c.name, got, c.start)
		}
		if got := c.b.PeriodEnd(c.now); !got.Equal(c.end) {
			t.Errorf("%s: end %v want %v", c.name, got, c.end)
		}
		if got := c.b.ResetsInDays(c.now); got != c.days {
			t.Errorf("%s: resets in %d want %d", c.name, got, c.days)
		}
	}
}

func TestBudgetDSTCountsCalendarDays(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	b := Budget{Enabled: true, LimitMB: 100, Period: "week", ResetDay: 1}
	now := time.Date(2025, 3, 8, 22, 0, 0, 0, ny) // Saturday before the spring-forward Sunday
	if got := b.ResetsInDays(now); got != 2 {
		t.Fatalf("resets in %d days, want 2 (Sat, Sun then Monday)", got)
	}
	if s := b.PeriodStart(now); s.Weekday() != time.Monday || s.Hour() != 0 {
		t.Fatalf("start %v", s)
	}
}

func TestBudgetStatusAndNormalize(t *testing.T) {
	b, err := ParseBudget(map[string]any{"enabled": true, "limitMB": float64(100), "period": "year", "resetDay": float64(99), "warnAt": []any{float64(90), float64(70), float64(70), float64(0), float64(150)}})
	if err != nil {
		t.Fatal(err)
	}
	if b.Period != "month" || b.ResetDay != 28 || len(b.WarnAt) != 2 || b.WarnAt[0] != 70 || b.WarnAt[1] != 90 {
		t.Fatalf("normalised %+v", b)
	}
	now := at(2026, 3, 10, 9)
	st := b.StatusFor(39*MB, now)
	if st.Pct < 38.9 || st.Pct > 39.1 || st.Exhausted || st.LeftBytes != 61*MB || st.LimitBytes != 100*MB {
		t.Fatalf("status %+v", st)
	}
	if st := b.StatusFor(100*MB, now); !st.Exhausted || st.LeftBytes != 0 {
		t.Fatalf("exhausted %+v", st)
	}
	off := b
	off.Enabled = false
	if st := off.StatusFor(5*MB, now); st.Pct != 0 || st.Exhausted || st.LimitBytes != 0 || st.UsedBytes != 5*MB {
		t.Fatalf("unlimited %+v", st)
	}
	if _, err := ParseBudget("nope"); err == nil {
		t.Fatal("non-object accepted")
	}
}

func openUsage(t *testing.T, dir string, now *time.Time) *DataUsage {
	t.Helper()
	b, err := NewDiskBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	u := OpenDataUsage(b, func() time.Time { return *now })
	u.Debounce = 0
	return u
}

// thresholds: each fires exactly once per period, a new period re-arms them,
// and a clock that jumps back into a past period does not repeat them.
func TestThresholdsOncePerPeriodAndClockChanges(t *testing.T) {
	now := at(2026, 3, 10, 9)
	u := openUsage(t, t.TempDir(), &now)
	b := Budget{Enabled: true, LimitMB: 100, Period: "month", ResetDay: 1, WarnAt: []int{70, 90}}
	check := func() []ThresholdEvent {
		st := b.StatusFor(u.Totals(b.PeriodStart(now), b.PeriodEnd(now)).Used, now)
		return u.Check(b, st)
	}
	u.Add(50*MB, map[string]int64{"https://a.example": 50 * MB}, 0, 0)
	if ev := check(); len(ev) != 0 {
		t.Fatalf("50%%: %v", ev)
	}
	u.Add(25*MB, nil, 0, 0)
	if ev := check(); len(ev) != 1 || ev[0] != (ThresholdEvent{"warn", 70}) {
		t.Fatalf("75%%: %v", ev)
	}
	if ev := check(); len(ev) != 0 {
		t.Fatalf("70 fired twice: %v", ev)
	}
	u.Add(20*MB, nil, 0, 0) // 95
	if ev := check(); len(ev) != 1 || ev[0].Pct != 90 {
		t.Fatalf("95%%: %v", ev)
	}
	u.Add(10*MB, nil, 0, 0) // 105
	if ev := check(); len(ev) != 1 || ev[0].Kind != "exhausted" {
		t.Fatalf("105%%: %v", ev)
	}
	u.Add(10*MB, nil, 0, 0)
	if ev := check(); len(ev) != 0 {
		t.Fatalf("exhausted fired twice: %v", ev)
	}
	// jump to a day of the next period: usage there is 0, thresholds are armed again
	now = at(2026, 4, 2, 9)
	if ev := check(); len(ev) != 0 {
		t.Fatalf("new period start: %v", ev)
	}
	u.Add(75*MB, nil, 0, 0)
	if ev := check(); len(ev) != 1 || ev[0].Pct != 70 {
		t.Fatalf("new period 75%%: %v", ev)
	}
	// the clock is set back into March: March is already announced, nothing repeats
	now = at(2026, 3, 20, 9)
	if ev := check(); len(ev) != 0 {
		t.Fatalf("clock set back repeated thresholds: %v", ev)
	}
	// and forward again into April: still once
	now = at(2026, 4, 3, 9)
	if ev := check(); len(ev) != 0 {
		t.Fatalf("clock forward repeated thresholds: %v", ev)
	}
}

func TestThresholdsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	now := at(2026, 3, 10, 9)
	b := Budget{Enabled: true, LimitMB: 100, Period: "month", ResetDay: 1, WarnAt: []int{70, 90}}
	u := openUsage(t, dir, &now)
	u.Add(80*MB, map[string]int64{"https://x.example": 80 * MB}, 5*MB, 7*MB)
	st := b.StatusFor(u.Totals(b.PeriodStart(now), b.PeriodEnd(now)).Used, now)
	if ev := u.Check(b, st); len(ev) != 1 {
		t.Fatalf("%v", ev)
	}
	u2 := openUsage(t, dir, &now)
	tot := u2.Totals(b.PeriodStart(now), b.PeriodEnd(now))
	if tot.Used != 80*MB || tot.SavedM != 5*MB || tot.SavedE != 7*MB {
		t.Fatalf("restored %+v", tot)
	}
	if ev := u2.Check(b, st); len(ev) != 0 {
		t.Fatalf("restart re-fired %v", ev)
	}
	if top := u2.Top(5, b.PeriodStart(now), b.PeriodEnd(now)); len(top) != 1 || top[0].Origin != "https://x.example" {
		t.Fatalf("top %v", top)
	}
}

func TestLimitChangeRebaselines(t *testing.T) {
	now := at(2026, 3, 10, 9)
	u := openUsage(t, t.TempDir(), &now)
	b := Budget{Enabled: true, LimitMB: 100, Period: "month", ResetDay: 1, WarnAt: []int{70, 90}}
	u.Add(75*MB, nil, 0, 0)
	st := b.StatusFor(75*MB, now)
	u.Rebaseline(b, st) // saving the budget never answers with a toast
	if ev := u.Check(b, st); len(ev) != 0 {
		t.Fatalf("%v", ev)
	}
	b.LimitMB = 500 // raised: 15%, thresholds are armed again
	st = b.StatusFor(75*MB, now)
	if ev := u.Check(b, st); len(ev) != 0 {
		t.Fatalf("limit change fired %v", ev)
	}
	u.Add(280*MB, nil, 0, 0) // 355 MB = 71%
	st = b.StatusFor(355*MB, now)
	if ev := u.Check(b, st); len(ev) != 1 || ev[0].Pct != 70 {
		t.Fatalf("after raise: %v", ev)
	}
}

func TestIncognitoCountsButStoresNoOrigins(t *testing.T) {
	now := at(2026, 3, 10, 9)
	// private profile: nothing touches a file
	mem := NewMemBackend()
	u := OpenDataUsage(mem, func() time.Time { return now })
	u.Add(3*MB, map[string]int64{"https://seen.example": 3 * MB}, 0, 0)
	u.AddIncognito(2 * MB)
	if _, err := mem.ReadFile(dataUsageFile); err == nil {
		t.Fatal("a memory profile wrote the usage file")
	}
	// persistent profile: private bytes land in the total but nowhere else
	dir := t.TempDir()
	d := openUsage(t, dir, &now)
	d.Add(1*MB, map[string]int64{"https://normal.example": 1 * MB}, 0, 0)
	d.AddIncognito(4 * MB)
	b := Budget{Enabled: true, LimitMB: 100, Period: "month", ResetDay: 1}
	tot := d.Totals(b.PeriodStart(now), b.PeriodEnd(now))
	if tot.Used != 5*MB || tot.Normal != 1*MB || tot.Incog != 4*MB {
		t.Fatalf("%+v", tot)
	}
	raw, err := os.ReadFile(filepath.Join(dir, dataUsageFile))
	if err != nil {
		t.Fatal(err)
	}
	top := d.Top(10, b.PeriodStart(now), b.PeriodEnd(now))
	if len(top) != 1 || top[0].Bytes != 1*MB {
		t.Fatalf("private bytes leaked into the top list: %v", top)
	}
	if want := `"incog":4194304`; !contains(string(raw), want) {
		t.Fatalf("incognito total missing from %s", raw)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestUsageCrashSafety(t *testing.T) {
	dir := t.TempDir()
	now := at(2026, 3, 10, 9)
	u := openUsage(t, dir, &now)
	u.Add(10*MB, nil, 0, 0)
	// a torn write must never be visible: the writer is atomic, so a leftover
	// temp file and a corrupt main file are both survivable
	if err := os.WriteFile(filepath.Join(dir, dataUsageFile+".tmp"), []byte(`{"v":1,"days":{"2026-03-1`), 0o600); err != nil {
		t.Fatal(err)
	}
	u2 := openUsage(t, dir, &now)
	if got := u2.Today().Total; got != 10*MB {
		t.Fatalf("restored %d", got)
	}
	if err := os.WriteFile(filepath.Join(dir, dataUsageFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	u3 := openUsage(t, dir, &now)
	if got := u3.Today().Total; got != 0 {
		t.Fatalf("corrupt file should start empty, got %d", got)
	}
	u3.Add(MB, nil, 0, 0) // and keep counting
	if got := openUsage(t, dir, &now).Today().Total; got != MB {
		t.Fatalf("after corrupt start %d", got)
	}
}

func TestUsageDebouncedFlush(t *testing.T) {
	dir := t.TempDir()
	now := at(2026, 3, 10, 9)
	b, _ := NewDiskBackend(dir)
	defer b.Close()
	u := OpenDataUsage(b, func() time.Time { return now })
	u.Debounce = time.Hour
	u.Add(MB, nil, 0, 0)
	if _, err := os.Stat(filepath.Join(dir, dataUsageFile)); err == nil {
		t.Fatal("written before the debounce")
	}
	u.Flush()
	if _, err := os.Stat(filepath.Join(dir, dataUsageFile)); err != nil {
		t.Fatal("Flush did not write")
	}
}

func TestUsageTopOriginsTrimAndPruneAndReset(t *testing.T) {
	now := at(2026, 3, 10, 9)
	u := openUsage(t, t.TempDir(), &now)
	for i := 0; i < 100; i++ {
		u.Add(int64(i+1)*1000, map[string]int64{"https://s" + itoa3(i) + ".example": int64(i+1) * 1000}, 0, 0)
	}
	d := u.doc.Days[DayKey(now)]
	if len(d.Origins) > dataOriginsPerDay*2 {
		t.Fatalf("origins unbounded: %d", len(d.Origins))
	}
	var sum int64 = d.Other
	for _, v := range d.Origins {
		sum += v
	}
	if sum != d.Total {
		t.Fatalf("trimming lost bytes: %d vs %d", sum, d.Total)
	}
	top := u.Top(5, now.AddDate(0, 0, -1), now.AddDate(0, 0, 1))
	if len(top) != 5 || top[0].Bytes < top[1].Bytes {
		t.Fatalf("top %v", top)
	}
	u.Reset()
	if u.Today().Total != 0 {
		t.Fatal("reset kept usage")
	}
	// history older than the keep window is dropped
	for i := 0; i < dataKeepDays+30; i++ {
		now = now.AddDate(0, 0, 1)
		u.Add(1, nil, 0, 0)
	}
	if len(u.doc.Days) > dataKeepDays+1 {
		t.Fatalf("history not pruned: %d days", len(u.doc.Days))
	}
}

func itoa3(i int) string { return string(rune('a'+i/26%26)) + string(rune('a'+i%26)) }

func TestDataSettingsRoundTrip(t *testing.T) {
	b := NewMemBackend()
	s, _ := OpenSettings(b)
	if s.DataLevel() != "off" {
		t.Fatalf("default level without a budget: %s", s.DataLevel())
	}
	if err := s.Set("data.budget", map[string]any{"enabled": true, "limitMB": float64(500), "period": "week", "resetDay": float64(3), "hardStop": true}); err != nil {
		t.Fatal(err)
	}
	if s.DataLevel() != "standard" {
		t.Fatalf("auto level with a budget: %s", s.DataLevel())
	}
	got := s.Budget()
	if got.LimitMB != 500 || got.Period != "week" || got.ResetDay != 3 || !got.HardStop || len(got.WarnAt) != 2 {
		t.Fatalf("%+v", got)
	}
	if err := s.Set("data.level", "strict"); err != nil || s.DataLevel() != "strict" {
		t.Fatal(err, s.DataLevel())
	}
	if err := s.Set("data.level", "turbo"); err == nil {
		t.Fatal("bad level accepted")
	}
	if err := s.Set("data.sites", map[string]any{"https://News.Example/path": "lite", "https://b.example": "full", "junk": "lite", "https://c.example": "weird"}); err != nil {
		t.Fatal(err)
	}
	if m := s.DataSites(); len(m) != 2 || m["https://news.example"] != "lite" || m["https://b.example"] != "full" {
		t.Fatalf("%v", m)
	}
	// reload from the same backend file: objects survive JSON
	disk := t.TempDir()
	db, _ := NewDiskBackend(disk)
	s1, _ := OpenSettings(db)
	_ = s1.Set("data.budget", map[string]any{"enabled": true, "limitMB": float64(1024)})
	_ = s1.Set("data.sites", map[string]any{"https://z.example": "full"})
	s2, _ := OpenSettings(db)
	if s2.Budget().LimitMB != 1024 || s2.DataSites()["https://z.example"] != "full" {
		t.Fatalf("%+v %v", s2.Budget(), s2.DataSites())
	}
	if !s2.Bool("data.agentLite") || !s2.Bool("data.showMeter") {
		t.Fatal("defaults for agentLite / showMeter should be true")
	}
}
