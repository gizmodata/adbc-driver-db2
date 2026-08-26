package drda

import (
	"fmt"
	"math/big"
	"time"
)

// Decimal is an exact DECIMAL/NUMERIC value: Unscaled × 10^-Scale.
type Decimal struct {
	Unscaled *big.Int
	Scale    int32
}

func (d Decimal) String() string {
	s := d.Unscaled.String()
	if d.Scale <= 0 {
		return s
	}
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	}
	for len(s) <= int(d.Scale) {
		s = "0" + s
	}
	i := len(s) - int(d.Scale)
	out := s[:i] + "." + s[i:]
	if neg {
		out = "-" + out
	}
	return out
}

// Date is a calendar date without a time zone.
type Date struct{ Year, Month, Day int }

func (d Date) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day) }

// DaysSinceEpoch returns the number of days between 1970-01-01 and d.
func (d Date) DaysSinceEpoch() int32 {
	t := time.Date(d.Year, time.Month(d.Month), d.Day, 0, 0, 0, 0, time.UTC)
	return int32(t.Unix() / 86400)
}

// Time is a wall-clock time without a date or zone.
type Time struct{ Hour, Minute, Second int }

func (t Time) String() string { return fmt.Sprintf("%02d:%02d:%02d", t.Hour, t.Minute, t.Second) }

// Timestamp is a date + time + fractional seconds (nanosecond precision;
// Db2 supports up to 12 fractional digits, which we truncate to 9).
type Timestamp struct {
	Date
	Time
	Nanos int
}

// ToTime converts to a UTC time.Time.
func (ts Timestamp) ToTime() time.Time {
	return time.Date(ts.Year, time.Month(ts.Month), ts.Day, ts.Hour, ts.Minute, ts.Second, ts.Nanos, time.UTC)
}

// Row values decoded from QRYDTA are one of:
//
//	nil (SQL NULL), bool, int16, int32, int64, float32, float64,
//	Decimal, string, []byte, Date, Time, Timestamp, LobRef
type Value = any

// LobRef is a placeholder for a LOB column whose bytes arrive out of
// line (EXTDTA). Resolved by the query reader into []byte / string.
type LobRef struct {
	IsChar bool
}
