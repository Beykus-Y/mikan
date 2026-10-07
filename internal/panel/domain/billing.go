package domain

import (
	"database/sql"
	"time"
)

// A billing day (1–31) ends every term on that day of the month: "+1 month" moves the
// expiry from the 10th to the next 10th, not by 30 days. A shorter month ends the term on
// its last day. All dates are UTC; a term keeps the time of day it started with.

// MinFirstTerm: a term that would end on the nearest billing day sooner than this runs to
// the billing day after it — the first payment covers at least half a month.
const MinFirstTerm = 15 * 24 * time.Hour

// dayOfMonth is day d of the month at clock's time of day, or the month's last day.
func dayOfMonth(year int, month time.Month, d int, clock time.Time) time.Time {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	return time.Date(year, month, min(d, last), clock.Hour(), clock.Minute(), clock.Second(), 0, time.UTC)
}

// NextBillingDate is the first day-d moment after t, at t's time of day.
func NextBillingDate(t time.Time, d int) time.Time {
	t = t.UTC()
	next := dayOfMonth(t.Year(), t.Month(), d, t)
	if !next.After(t) {
		next = dayOfMonth(t.Year(), t.Month()+1, d, t)
	}
	return next
}

// nextTerm is the billing day that ends a month started at base: the nearest one at least
// MinFirstTerm away.
func nextTerm(base time.Time, d int) time.Time {
	e := NextBillingDate(base, d)
	if e.Sub(base) < MinFirstTerm {
		e = NextBillingDate(e, d)
	}
	return e
}

// AddMonths extends a term by n months from base (the current expiry, or now when the
// term already ended): to the n-th term end on the billing day, or without one to the
// same day of the month n months later.
func AddMonths(base time.Time, n int, billingDay sql.NullInt64) time.Time {
	base = base.UTC()
	if !billingDay.Valid {
		return dayOfMonth(base.Year(), base.Month()+time.Month(n), base.Day(), base)
	}
	d := int(billingDay.Int64)
	e := nextTerm(base, d)
	for i := 1; i < n; i++ {
		e = NextBillingDate(e, d)
	}
	return e
}

// TermMonths turns a tariff's duration into months for a billing day: 30 days is one.
func TermMonths(days int64) int {
	return max(1, int((days+15)/30))
}

// tariffExpiry is when a term on tariff t that starts now ends (invalid = never).
func tariffExpiry(now time.Time, t durationTariff) sql.NullInt64 {
	if t.days <= 0 {
		return sql.NullInt64{}
	}
	if t.billingDay.Valid {
		return sql.NullInt64{Int64: AddMonths(now, TermMonths(t.days), t.billingDay).Unix(), Valid: true}
	}
	return expiry(now.Unix(), t.days)
}

type durationTariff struct {
	days       int64
	billingDay sql.NullInt64
}

// MonthPeriodStart is when the current monthly traffic period began: the last billing
// day (or the 1st without one) at 00:00 UTC not after now.
func MonthPeriodStart(now time.Time, billingDay sql.NullInt64) time.Time {
	d := 1
	if billingDay.Valid {
		d = int(billingDay.Int64)
	}
	now = now.UTC()
	start := dayOfMonth(now.Year(), now.Month(), d, time.Time{})
	if start.After(now) {
		start = dayOfMonth(now.Year(), now.Month()-1, d, time.Time{})
	}
	return start
}

// nextMonthPeriod is when the monthly traffic period that began at start ends.
func nextMonthPeriod(start time.Time, billingDay sql.NullInt64) time.Time {
	d := 1
	if billingDay.Valid {
		d = int(billingDay.Int64)
	}
	return dayOfMonth(start.Year(), start.Month()+1, d, time.Time{})
}

// ValidBillingDay: 1–31.
func ValidBillingDay(d int64) bool { return d >= 1 && d <= 31 }
