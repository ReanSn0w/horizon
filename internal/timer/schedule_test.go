package timer

import (
	"strings"
	"testing"
	"time"
)

func instant(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func nextSlot(t *testing.T, config ScheduleConfig, after, at, deadline string) {
	t.Helper()
	schedule, err := Compile(config)
	if err != nil {
		t.Fatal(err)
	}
	got, err := schedule.Next(instant(t, after))
	if err != nil || !got.At.Equal(instant(t, at)) {
		t.Fatalf("next = %v, %v, want %s", got.At, err, at)
	}
	if deadline == "" {
		if got.Deadline != nil {
			t.Fatalf("unexpected deadline %v", got.Deadline)
		}
		return
	}
	if got.Deadline == nil || !got.Deadline.Equal(instant(t, deadline)) {
		t.Fatalf("deadline = %v, want %s", got.Deadline, deadline)
	}
}

func TestEveryEpochGridAndWindows(t *testing.T) {
	nextSlot(t, ScheduleConfig{Every: "1m30.5s", Timezone: "UTC"},
		"1970-01-01T00:01:30.5Z", "1970-01-01T00:03:01Z", "")
	nextSlot(t, ScheduleConfig{Every: "90s", Timezone: "UTC"},
		"1969-12-31T23:58:10Z", "1969-12-31T23:58:30Z", "")
	nextSlot(t, ScheduleConfig{Every: "61s", Timezone: "UTC", Windows: []WindowConfig{{Start: "00:02", End: "00:03"}}},
		"1970-01-01T00:01:59Z", "1970-01-01T00:02:02Z", "1970-01-01T00:03:00Z")
	config := ScheduleConfig{Every: "15m", Timezone: "UTC", Windows: []WindowConfig{{"23:45", "01:00"}, {"00:40", "02:00"}}}
	nextSlot(t, config, "2026-01-01T23:44:59Z", "2026-01-01T23:45:00Z", "2026-01-02T02:00:00Z")
	nextSlot(t, config, "2026-01-02T01:59:59Z", "2026-01-02T23:45:00Z", "2026-01-03T02:00:00Z")
}

func TestCronCalendarAndDST(t *testing.T) {
	utc := func(cron string) ScheduleConfig { return ScheduleConfig{Cron: cron, Timezone: "UTC"} }
	nextSlot(t, utc("0,20-30/5 9-11/2 * 2 7"), "2026-02-01T09:21:00Z", "2026-02-01T09:25:00Z", "")
	nextSlot(t, utc("0 10 13 * 1"), "2026-02-12T00:00:00Z", "2026-02-13T10:00:00Z", "")
	nextSlot(t, utc("0 10 13 * 1"), "2026-02-14T00:00:00Z", "2026-02-16T10:00:00Z", "")
	nextSlot(t, utc("0 10 */2 * 1"), "2026-02-01T10:00:00Z", "2026-02-02T10:00:00Z", "")
	nextSlot(t, utc("0 10 * * 1"), "2026-02-02T10:00:00Z", "2026-02-09T10:00:00Z", "")
	nextSlot(t, utc("0 0 29 2 *"), "2026-01-01T00:00:00Z", "2028-02-29T00:00:00Z", "")
	nextSlot(t, ScheduleConfig{Cron: "30 2 * * *", Timezone: "America/New_York"},
		"2026-03-08T05:00:00Z", "2026-03-09T06:30:00Z", "")
	fall := ScheduleConfig{Cron: "30 1 * * *", Timezone: "America/New_York", Windows: []WindowConfig{{"01:00", "02:00"}}}
	nextSlot(t, fall, "2026-11-01T04:00:00Z", "2026-11-01T05:30:00Z", "2026-11-01T07:00:00Z")
	nextSlot(t, fall, "2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z", "2026-11-02T07:00:00Z")
	nextSlot(t, ScheduleConfig{Every: "30m", Timezone: "America/New_York"},
		"2026-11-01T05:30:00Z", "2026-11-01T06:00:00Z", "")
}

func TestInvalidAndImpossibleSchedules(t *testing.T) {
	for _, config := range []ScheduleConfig{
		{Every: "59s", Timezone: "UTC"}, {Every: "0s", Timezone: "UTC"}, {Every: "-2m", Timezone: "UTC"},
		{Every: "9999999999999999h", Timezone: "UTC"}, {Every: "1m", Timezone: "Local"},
		{Every: "1m", Timezone: "Unknown/City"}, {Every: "1m"},
		{Every: "1m", Cron: "* * * * *", Timezone: "UTC"},
		{Cron: "@weekly", Timezone: "UTC"}, {Cron: "0 * * * * *", Timezone: "UTC"},
		{Cron: "*/0 * * * *", Timezone: "UTC"}, {Cron: "0 0 * 0 *", Timezone: "UTC"},
		{Cron: "0 9 * * *", Timezone: "UTC", Windows: []WindowConfig{{"11:00", "12:00"}}},
		{Cron: "0 0 * * 8", Timezone: "UTC"}, {Cron: "0 0 * * 7-0", Timezone: "UTC"},
		{Every: "1m", Timezone: "UTC", Windows: []WindowConfig{{"01:00", "01:00"}}},
		{Every: "1m", Timezone: "UTC", Windows: []WindowConfig{{"24:00", "01:00"}}},
	} {
		if _, err := Compile(config); err == nil {
			t.Errorf("accepted %+v", config)
		}
	}
	for _, config := range []ScheduleConfig{
		{Cron: "0 9 31 2 *", Timezone: "UTC"},
		{Every: "24h", Timezone: "UTC", Windows: []WindowConfig{{"09:00", "10:00"}}},
	} {
		schedule, err := Compile(config)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := schedule.Next(instant(t, "2026-01-01T00:00:00Z")); err == nil || !strings.Contains(err.Error(), "search horizon") {
			t.Fatalf("impossible %+v: %v", config, err)
		}
	}
}
