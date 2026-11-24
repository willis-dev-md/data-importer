// Package schedule is the pure schedule engine: validation of a source's schedule, next-run computation in the
// source's timezone, the missed-slot decision and the deterministic jitter offset. It has no database and no clock:
// every function takes the instants it needs, so tests use fixed instants.
//
// Decisions (docs/design.md, Scheduler): only daily, weekly and monthly (no cron); the default timezone is
// Europe/London; a failed run is not retried (the next slot is the retry); cadence is computed from the scheduled
// slot, never from when a run finished.
package schedule
