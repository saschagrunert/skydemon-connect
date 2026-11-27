package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/feed"
	"github.com/saschagrunert/skydemon-connect/internal/simconnect"
)

const (
	statusWidth = 70
	// statusEvery is how often the console status line is redrawn.
	statusEvery = time.Second
)

// statusLine shares one terminal between a status line, rewritten in
// place, and log lines, which clear it first so that they don't run on
// from it.
type statusLine struct {
	out   io.Writer
	mu    sync.Mutex
	shown bool
}

// Write writes a log line.
func (s *statusLine) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.shown {
		_, _ = io.WriteString(s.out, clearLine())
		s.shown = false
	}

	return s.out.Write(p)
}

// clearLine blanks the status line and returns to its start.
func clearLine() string { return "\r" + strings.Repeat(" ", statusWidth) + "\r" }

func (s *statusLine) show(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Cut to the cleared width, so that a long status, e.g. an error,
	// leaves nothing behind when a shorter one replaces it.
	if r := []rune(text); len(r) > statusWidth {
		text = string(r[:statusWidth-1]) + "…"
	}

	_, _ = fmt.Fprintf(s.out, "\r%-*s", statusWidth, text)
	s.shown = true
}

func (s *statusLine) end() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.shown {
		_, _ = fmt.Fprintln(s.out)
		s.shown = false
	}
}

// runConsoleUntilInterrupt runs the console mode on out until Ctrl+C, or,
// so that the SimConnect session and the COM port are closed properly,
// until the console window is closed or the session ends. On Windows Go
// turns closing the console into SIGTERM; logoff and shutdown come from
// onSessionEnd, which waits until the app shut down.
func runConsoleUntilInterrupt(c *config, out io.Writer) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	done := make(chan struct{})

	onSessionEnd(func() {
		cancel()
		<-done
	})

	err := runConsole(ctx, c, out, simconnect.Run, statusEvery)

	close(done)

	return err
}

// runConsole runs with a status line on out, redrawn every interval,
// instead of the tray, until ctx is done or the feed asks to quit. It
// returns why it could not start.
func runConsole(
	ctx context.Context,
	c *config,
	out io.Writer,
	sim simFunc,
	every time.Duration,
) error {
	status := &statusLine{out: out}
	l := log.New(status, "", log.Ltime)

	l.Printf("SkyDemonConnect %s, %d Hz", version, feed.RateHz)

	// With -auto, as with the tray, MSFS's exit ends the app.
	watched := false
	if c.auto {
		ctx, watched = watchMSFS(ctx, l)
	}

	r, err := startFeed(
		ctx,
		c,
		input{sim: sim, quitOnSessionEnd: sessionEndQuits(c.auto, watched)},
		l,
	)
	if err != nil {
		return err
	}
	defer r.stop()

	tick := time.NewTicker(every)
	defer tick.Stop()

	for {
		select {
		case <-r.quit:
			status.end()

			return nil
		case <-tick.C:
			if !c.quiet {
				status.show(r.feed.Status().String())
			}
		}
	}
}
