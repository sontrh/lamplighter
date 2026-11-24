package main

import (
	"time"
)

const (
	// A job that was skipped at no cost is looked at again after skipFirst,
	// then twice as long each time, up to skipMax.
	skipFirst = time.Minute
	skipMax   = time.Hour

	// A job that cost money is left alone for lossFirst, then twice as long
	// each time. After maxLosses it is not touched again until restart.
	lossFirst = 10 * time.Minute
	maxLosses = 3

	// maxRecords bounds the memory a flood of jobs can take.
	maxRecords = 10000
)

// ledger is the bot's memory between passes: which jobs to leave alone for
// now, and how much has been lost.
//
// Without it a job that passes simulation and fails in its block would cost
// the fee again every pass, and a job that never pays would be simulated
// again every pass.
type ledger struct {
	now     func() time.Time
	records map[string]*record
	lost    int64 // ugnot spent on runs that paid less than planned
}

type record struct {
	skips  int
	losses int
	until  time.Time
	banned bool
}

func newLedger(now func() time.Time) *ledger {
	return &ledger{now: now, records: map[string]*record{}}
}

// resting reports whether a job should be left alone right now.
func (l *ledger) resting(id string) bool {
	r, ok := l.records[id]
	return ok && (r.banned || l.now().Before(r.until))
}

// skipped records that looking at a job cost nothing and led nowhere.
func (l *ledger) skipped(id string) time.Duration {
	r := l.record(id)
	wait := skipFirst << min(r.skips, 10)
	if wait > skipMax {
		wait = skipMax
	}
	r.skips++
	r.until = l.now().Add(wait)
	return wait
}

// lostOn records that a run of a job cost amount ugnot more than planned. It
// reports how long the job will rest and whether it is now banned.
func (l *ledger) lostOn(id string, amount int64) (time.Duration, bool) {
	if amount > 0 {
		l.lost += amount
	}
	r := l.record(id)
	wait := lossFirst << min(r.losses, 10)
	r.losses++
	r.until = l.now().Add(wait)
	r.banned = r.losses >= maxLosses
	return wait, r.banned
}

// paid records a run that went as planned: the job starts with a clean sheet.
func (l *ledger) paid(id string) {
	delete(l.records, id)
}

func (l *ledger) record(id string) *record {
	if r, ok := l.records[id]; ok {
		return r
	}
	if len(l.records) >= maxRecords {
		l.prune()
	}
	r := &record{}
	l.records[id] = r
	return r
}

// prune drops the records that no longer hold a job back. If every record
// still does, it drops them all: forgetting a backoff costs a few wasted
// simulations, while an unbounded map costs the process.
func (l *ledger) prune() {
	now := l.now()
	for id, r := range l.records {
		if !r.banned && !now.Before(r.until) {
			delete(l.records, id)
		}
	}
	if len(l.records) >= maxRecords {
		l.records = map[string]*record{}
	}
}
