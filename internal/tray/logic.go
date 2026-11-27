package tray

import (
	"strings"

	"github.com/saschagrunert/skydemon-connect/internal/exexml"
	"github.com/saschagrunert/skydemon-connect/internal/feed"
	"github.com/saschagrunert/skydemon-connect/internal/icon"
)

// The tray's portable parts: colours, state tracking, the menu and texts,
// tested on any OS.

// Menu command IDs.
const (
	idMSFS = 100
	idLog  = 101
	idQuit = 999
)

// colour returns the icon colour of a state.
func colour(kind feed.Kind) icon.RGB {
	switch kind {
	case feed.Sending:
		return icon.Green
	case feed.Failed:
		return icon.Red
	case feed.Waiting:
	}

	return icon.Grey
}

// kinds are the states, each with its icon.
func kinds() []feed.Kind { return []feed.Kind{feed.Waiting, feed.Sending, feed.Failed} }

// tracker follows the feed's state between refreshes.
type tracker struct {
	last    feed.Kind
	lastErr string
	shown   bool
}

// update records the state kind, with its error, and reports whether
// either changed: a change is logged, and a change to Failed, or to
// another problem, shown in a balloon.
func (t *tracker) update(kind feed.Kind, err string) bool {
	if t.shown && kind == t.last && err == t.lastErr {
		return false
	}

	t.shown, t.last, t.lastErr = true, kind, err

	return true
}

// IconHint tells where to find the icon, which Windows 11 hides at first.
const IconHint = "Its icon is in the system tray (click ^ if it is hidden)."

// Balloon texts.
const (
	welcomeTitle = "SkyDemonConnect is running"
	welcomeText  = IconHint + " Click it for Start with MSFS, the log and Quit. " +
		"In SkyDemon: Go Flying → Connect to X-Plane."
	problemTitle = "SkyDemonConnect: problem"
	addedTitle   = "Starts with MSFS"
	addedText    = "From the next MSFS start, MSFS launches it and it quits when MSFS closes. " +
		"Keep the exe in this folder."
	addedArgs    = "Options: "
	removedTitle = "No longer starts with MSFS"
	removedText  = "MSFS no longer launches SkyDemonConnect."
	// noLogText is shown for Open log when no folder took the log.
	noLogText = "No log: neither the exe's folder nor %LOCALAPPDATA%\\SkyDemonConnect is writable."
)

// menuItem is one line of the tray menu; id 0 is a disabled line.
type menuItem struct {
	text      string
	id        int
	checked   bool
	separator bool
}

// Start with MSFS menu texts.
const (
	startText = "Start with MSFS"
	// otherText: an entry exists for another copy or other options, or is
	// disabled; clicking replaces it with this copy's.
	otherText = "Start with MSFS (another or disabled entry)"
	// noExeText: without the exe's own path, there is nothing to write.
	noExeText = "Start with MSFS (exe path unknown)"
)

// menuState is what the tray menu shows.
type menuState struct {
	version, status string
	// destinations are where positions go, e.g. "UDP: 127.0.0.1".
	destinations []string
	msfs         exexml.State
	// canStart: the exe's path is known, so Start with MSFS can work.
	canStart bool
}

// menu returns the tray menu.
func menu(s *menuState) []menuItem {
	items := []menuItem{
		{text: "SkyDemonConnect " + s.version},
		{text: s.status},
	}

	for _, d := range s.destinations {
		items = append(items, menuItem{text: d})
	}

	start := menuItem{text: startText, id: idMSFS, checked: s.msfs == exexml.Ours}

	switch {
	case !s.canStart:
		start = menuItem{text: noExeText}
	case s.msfs == exexml.Other:
		start.text = otherText
	}

	return append(items,
		menuItem{separator: true},
		start,
		menuItem{text: "Open log", id: idLog},
		menuItem{separator: true},
		menuItem{text: "Quit", id: idQuit},
	)
}

// addedMessage is the balloon text after Start with MSFS wrote args to
// exe.xml: they replace whatever an entry there had, so they are shown,
// first, so that the balloon's length limit cuts the general text instead.
func addedMessage(args []string) string {
	opts := "none"
	if len(args) > 0 {
		opts = strings.Join(args, " ")
	}

	return addedArgs + opts + ". " + addedText
}
