package timer

import (
	"errors"
	"fmt"
	"math/big"
	"math/bits"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

const (
	minutesPerDay = 24 * 60
	searchYears   = 8
)

var wallTime = regexp.MustCompile(`^[0-9]{2}:[0-9]{2}$`)

type Schedule struct {
	every   time.Duration
	cron    [5]cronField
	zone    *time.Location
	windows *[minutesPerDay]bool
}

type Slot struct {
	At       time.Time
	Deadline *time.Time
}

func Compile(cfg ScheduleConfig) (Schedule, error) {
	if (cfg.Every == "") == (cfg.Cron == "") {
		return Schedule{}, errors.New("schedule must set exactly one of every or cron")
	}
	if cfg.Timezone == "" || cfg.Timezone == "Local" {
		return Schedule{}, errors.New("schedule.timezone must be an explicit IANA timezone")
	}
	location, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return Schedule{}, fmt.Errorf("schedule.timezone %q: %w", cfg.Timezone, err)
	}
	s := Schedule{zone: location}
	if cfg.Every != "" {
		s.every, err = time.ParseDuration(cfg.Every)
		if err != nil || s.every < time.Minute {
			return Schedule{}, errors.New("schedule.every must be a Go duration of at least 1m")
		}
	} else {
		s.cron, err = parseCron(cfg.Cron)
		if err != nil {
			return Schedule{}, err
		}
	}
	if len(cfg.Windows) == 0 {
		return s, nil
	}
	var windows [minutesPerDay]bool
	for i, window := range cfg.Windows {
		start, err := parseClock(window.Start)
		if err != nil {
			return Schedule{}, fmt.Errorf("schedule.windows[%d].start: %w", i, err)
		}
		end, err := parseClock(window.End)
		if err != nil {
			return Schedule{}, fmt.Errorf("schedule.windows[%d].end: %w", i, err)
		}
		if start == end {
			return Schedule{}, fmt.Errorf("schedule.windows[%d]: start and end must differ", i)
		}
		for m := start; m != end; m = (m + 1) % minutesPerDay {
			windows[m] = true
		}
	}
	for _, open := range windows {
		if !open {
			s.windows = &windows
			break
		}
	}
	return s, nil
}

func parseClock(value string) (int, error) {
	if wallTime.MatchString(value) {
		h, _ := strconv.Atoi(value[:2])
		m, _ := strconv.Atoi(value[3:])
		if h < 24 && m < 60 {
			return h*60 + m, nil
		}
	}
	return 0, errors.New("expected HH:MM from 00:00 to 23:59")
}

type cronField uint64

func (f cronField) includes(value int) bool { return f&(1<<value) != 0 }

func parseCron(expr string) ([5]cronField, error) {
	var fields [5]cronField
	parts := strings.Fields(expr)
	if len(parts) != len(fields) {
		return fields, errors.New("schedule.cron requires five fields: minute hour day-of-month month day-of-week")
	}
	bounds := [5]struct {
		name     string
		min, max int
	}{
		{"minute", 0, 59},
		{"hour", 0, 23},
		{"day-of-month", 1, 31},
		{"month", 1, 12},
		{"day-of-week", 0, 7},
	}
	for i, bound := range bounds {
		field, err := parseCronField(parts[i], bound.min, bound.max)
		if err != nil {
			return fields, fmt.Errorf("schedule.cron %s: %w", bound.name, err)
		}
		fields[i] = field
	}
	if fields[4].includes(7) {
		fields[4] = (fields[4] &^ (1 << 7)) | 1
	}
	return fields, nil
}

func parseCronField(expr string, minValue, maxValue int) (cronField, error) {
	var field cronField
	for segment := range strings.SplitSeq(expr, ",") {
		rangeText, stepText, stepped := strings.Cut(segment, "/")
		step := 1
		if stepped {
			var err error
			step, err = number(stepText)
			if err != nil || step == 0 {
				return 0, fmt.Errorf("invalid step %q", stepText)
			}
		}
		first, last := minValue, maxValue
		if rangeText != "*" {
			start, end, ranged := strings.Cut(rangeText, "-")
			var err error
			first, err = number(start)
			if err != nil {
				return 0, err
			}
			switch {
			case ranged:
				last, err = number(end)
				if err != nil {
					return 0, err
				}
			case !stepped:
				last = first
			}
		}
		if first < minValue || last > maxValue || first > last {
			return 0, fmt.Errorf("%q is outside %d..%d", rangeText, minValue, maxValue)
		}
		for i := first; i <= last; i += step {
			field |= 1 << i
			if step > last-i {
				break
			}
		}
	}
	return field, nil
}

func number(s string) (int, error) {
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
		if n, err := strconv.Atoi(s); err == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("expected a nonnegative integer, got %q", s)
}

func (s Schedule) matchesDay(local time.Time) bool {
	if !s.cron[3].includes(int(local.Month())) {
		return false
	}
	day := s.cron[2].includes(local.Day())
	weekday := s.cron[4].includes(int(local.Weekday()))
	if bits.OnesCount64(uint64(s.cron[2])) == 31 || bits.OnesCount64(uint64(s.cron[4])) == 7 {
		return day && weekday
	}
	return day || weekday
}

func minuteOfDay(t time.Time) int { return t.Hour()*60 + t.Minute() }

func (s Schedule) open(t time.Time) bool {
	return s.windows == nil || s.windows[minuteOfDay(t)]
}

// Next returns the first matching slot strictly after the supplied wall clock.
// It is independent of process uptime and completion times.
func (s Schedule) Next(after time.Time) (Slot, error) {
	after = after.Round(0).In(s.zone)
	if s.every != 0 {
		at := firstInterval(after, s.every)
		limit := at.AddDate(searchYears, 0, 0)
		for !at.After(limit) {
			if s.open(at) {
				return s.slot(at), nil
			}
			at = at.Add(s.every)
		}
	} else {
		limit := after.AddDate(searchYears, 0, 0)
		for at := nextWallMinute(after); !at.After(limit); {
			if !s.matchesDay(at) {
				at = nextDayOrTransition(at)
				continue
			}
			if s.cron[0].includes(at.Minute()) && s.cron[1].includes(at.Hour()) && s.open(at) && !repeatedMinute(at) {
				return s.slot(at), nil
			}
			at = nextWallMinute(at)
		}
	}
	return Slot{}, fmt.Errorf("schedule has no allowed firing within the %d-year search horizon", searchYears)
}

func firstInterval(after time.Time, every time.Duration) time.Time {
	// UnixNano is undefined outside 1678..2262. Reduce the epoch offset
	// modulo the duration before converting it back to nanoseconds.
	var offset, remainder big.Int
	offset.SetInt64(after.Unix())
	offset.Mul(&offset, big.NewInt(int64(time.Second)))
	offset.Add(&offset, big.NewInt(int64(after.Nanosecond())))
	remainder.Mod(&offset, big.NewInt(int64(every)))
	return after.Add(every - time.Duration(remainder.Int64()))
}

func nextDayOrTransition(at time.Time) time.Time {
	next := at.Add(time.Duration(minutesPerDay-minuteOfDay(at)) * time.Minute)
	_, end := at.ZoneBounds()
	if !end.IsZero() && end.Before(next) {
		next = end
	}
	if next.Second() != 0 || next.Nanosecond() != 0 {
		next = nextWallMinute(next)
	}
	return next
}

func nextWallMinute(at time.Time) time.Time {
	for {
		next := at.Add(time.Minute - time.Duration(at.Second())*time.Second - time.Duration(at.Nanosecond()))
		_, end := at.ZoneBounds()
		if end.IsZero() || !end.Before(next) {
			if next.Second() == 0 && next.Nanosecond() == 0 {
				return next
			}
			at = next
			continue
		}
		if end.Second() == 0 && end.Nanosecond() == 0 {
			return end
		}
		at = end
	}
}

func repeatedMinute(at time.Time) bool {
	start, _ := at.ZoneBounds()
	if start.IsZero() {
		return false
	}
	_, current := at.Zone()
	_, previous := start.Add(-time.Nanosecond).Zone()
	return previous > current && at.Before(start.Add(time.Duration(previous-current)*time.Second))
}

func (s Schedule) slot(at time.Time) Slot {
	slot := Slot{At: at.UTC()}
	if s.windows == nil {
		return slot
	}
	for end := nextWallMinute(at); ; end = nextWallMinute(end) {
		if !s.open(end) {
			deadline := end.UTC()
			slot.Deadline = &deadline
			return slot
		}
	}
}
