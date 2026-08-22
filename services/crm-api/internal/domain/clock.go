package domain

import "time"

// storedTimePrecision is the resolution PostgreSQL's timestamptz keeps.
//
// Go's time.Now() carries nanoseconds; timestamptz stores microseconds. Every
// stamp this package writes therefore loses its sub-microsecond tail the
// moment it reaches the database.
const storedTimePrecision = time.Microsecond

// nowStamp returns the current UTC time at the precision the store can
// actually keep.
//
// Truncating here rather than letting the database do it silently is what
// keeps a record's timestamp honest. A create returns the record it just
// wrote, including UpdatedAt; if that value carried nanoseconds the database
// discarded, the object in the caller's hand would disagree with the row —
// and UpdateAccount, UpdateTask and their siblings compare UpdatedAt exactly
// to detect a concurrent edit. The result was a spurious conflict on any
// create-then-update against PostgreSQL whenever the clock landed off a
// microsecond boundary, which is almost always:
//
//	returned by Create : 2026-08-22T12:42:58.016017158Z
//	read back from DB  : 2026-08-22T12:42:58.016017Z
//
// MemStore truncates too, so the two implementations agree about what a
// stored timestamp is rather than diverging by a rounding rule that only one
// of them applies.
//
// Use this for anything that will be persisted or compared against a
// persisted value. time.Now() is still correct for measuring elapsed time,
// where the extra resolution is real.
func nowStamp() time.Time {
	return time.Now().UTC().Truncate(storedTimePrecision)
}
