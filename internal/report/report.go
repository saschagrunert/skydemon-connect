// Package report logs errors that repeat, such as a send failing 30 times a
// second, once when they start or change and once when they stop.
package report

import (
	"log"
	"sync"
)

// Reporter logs the errors of named activities once per change. The zero
// value is not usable; use New. It is safe for concurrent use.
type Reporter struct {
	log  *log.Logger
	last map[string]string
	mu   sync.Mutex
}

// New returns a Reporter that logs to l.
func New(l *log.Logger) *Reporter {
	return &Reporter{log: l, last: map[string]string{}}
}

// Forget drops what's error without logging, e.g. when what is no longer
// tried at all.
func (r *Reporter) Forget(what string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.last, what)
}

// Report records the outcome of the latest attempt at what: err, or nil for
// success. It logs "what: err" when the error is new or differs from the
// previous one, and "what: working again" on the first success after an
// error.
func (r *Reporter) Report(what string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	prev, failing := r.last[what]

	switch {
	case err == nil && failing:
		delete(r.last, what)
		r.log.Printf("%s: working again", what)
	case err != nil && (!failing || prev != err.Error()):
		r.last[what] = err.Error()
		r.log.Printf("%s: %v", what, err)
	}
}
