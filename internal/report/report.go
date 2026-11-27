// Package report logs errors that repeat, such as a send failing 30 times a
// second, once when they start or change and once when they stop.
package report

import (
	"log"
	"sync"
	"time"
)

// Reporter logs the errors of named activities once per change. The zero
// value is not usable; use New or NewSettled. It is safe for concurrent
// use.
type Reporter struct {
	log    *log.Logger
	keys   map[string]*activity
	settle time.Duration
	mu     sync.Mutex
}

// activity is what a Reporter knows about one activity.
type activity struct {
	// errSince is when the current run of errors began, okSince when the
	// current run of successes did; zero while the other kind runs.
	errSince, okSince time.Time
	// err is the error logged while failing.
	err     string
	failing bool
}

// New returns a Reporter that logs to l and counts an activity as failing
// from its first error until its first success.
func New(l *log.Logger) *Reporter { return NewSettled(l, 0) }

// NewSettled returns a Reporter that logs to l and counts an activity as
// failing only once it failed for settle without a success in between,
// and as working again once it succeeded for settle. An activity that
// flaps between both faster than that, e.g. a send failing every other
// time, neither logs nor fails on every flip: it never counts as failing,
// although up to half of its attempts may be lost.
func NewSettled(l *log.Logger, settle time.Duration) *Reporter {
	return &Reporter{log: l, keys: map[string]*activity{}, settle: settle}
}

// Forget drops what's state, e.g. when what is no longer tried, or is
// judged afresh. A logged failure it drops is logged as "what: reset", so
// that the log doesn't end with an error that is no longer known.
func (r *Reporter) Forget(what string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if state := r.keys[what]; state != nil && state.failing {
		r.log.Printf("%s: reset", what)
	}

	delete(r.keys, what)
}

// Failing reports whether what counts as failing.
func (r *Reporter) Failing(what string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	k := r.keys[what]

	return k != nil && k.failing
}

// Report records the outcome of the latest attempt at what: err, or nil for
// success. It logs "what: err" when what starts failing or fails with
// another error, and "what: working again" when it stops failing.
func (r *Reporter) Report(what string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.keys[what]
	if state == nil {
		if err == nil {
			return
		}

		state = &activity{}
		r.keys[what] = state
	}

	now := time.Now()

	if err == nil {
		recovered, done := state.succeeded(now, r.settle)
		if recovered {
			r.log.Printf("%s: working again", what)
		}

		if done {
			delete(r.keys, what)
		}

		return
	}

	if state.failed(now, r.settle, err) {
		r.log.Printf("%s: %v", what, err)
	}
}

// succeeded records a success at now. It reports whether that ends a
// failure, and whether the activity is done with, being neither failing nor
// on its way to.
func (k *activity) succeeded(now time.Time, settle time.Duration) (bool, bool) {
	k.errSince = time.Time{}
	if k.okSince.IsZero() {
		k.okSince = now
	}

	settled := now.Sub(k.okSince) >= settle

	return k.failing && settled, !k.failing || settled
}

// failed records err at now and reports whether it is to be logged: what
// starts failing, or fails with another error.
func (k *activity) failed(now time.Time, settle time.Duration, err error) bool {
	k.okSince = time.Time{}
	if k.errSince.IsZero() {
		k.errSince = now
	}

	if (k.failing && k.err == err.Error()) || (!k.failing && now.Sub(k.errSince) < settle) {
		return false
	}

	k.failing, k.err = true, err.Error()

	return true
}
