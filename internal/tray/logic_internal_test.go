package tray

import (
	"slices"
	"strings"
	"testing"

	"github.com/saschagrunert/skydemon-connect/internal/exexml"
	"github.com/saschagrunert/skydemon-connect/internal/feed"
)

func TestColour(t *testing.T) {
	t.Parallel()

	seen := map[[3]uint8]feed.Kind{}

	for _, kind := range kinds() {
		c := colour(kind)
		rgb := [3]uint8{c.R(), c.G(), c.B()}

		if other, ok := seen[rgb]; ok {
			t.Errorf("%v and %v share a colour", kind, other)
		}

		seen[rgb] = kind
	}

	if c := colour(feed.Sending); c.G() <= c.R() {
		t.Errorf("sending is not green: %06x", uint32(c))
	}

	if c := colour(feed.Failed); c.R() <= c.G() {
		t.Errorf("failed is not red: %06x", uint32(c))
	}
}

func TestTracker(t *testing.T) {
	t.Parallel()

	var trk tracker

	for i, step := range []struct {
		kind    feed.Kind
		err     string
		changed bool
	}{
		{feed.Waiting, "", true}, // the first state is logged
		{feed.Waiting, "", false},
		{feed.Sending, "", true},
		{feed.Failed, "a", true},
		{feed.Failed, "a", false}, // one balloon per problem
		{feed.Failed, "b", true},  // another problem gets its own
		{feed.Sending, "", true},
		{feed.Failed, "b", true},
	} {
		if changed := trk.update(step.kind, step.err); changed != step.changed {
			t.Errorf("step %d (%v): changed %v, want %v", i, step.kind, changed, step.changed)
		}
	}
}

// Balloon texts fit NOTIFYICONDATAW's Info (256) and InfoTitle (64), and
// say MSFS, not "the sim".
func TestBalloonTexts(t *testing.T) {
	t.Parallel()

	for _, b := range [][2]string{
		{welcomeTitle, welcomeText},
		{addedTitle, addedText},
		{removedTitle, removedText},
		{problemTitle, ""},
		{"", noLogText}, // a message box, which allows more; checked as a balloon
	} {
		if len(b[0]) >= 64 || len(b[1]) >= 256 {
			t.Errorf("too long: %q", b)
		}

		if strings.Contains(b[0]+b[1], "sim ") {
			t.Errorf("says sim, not MSFS: %q", b)
		}
	}
}

func TestMenu(t *testing.T) {
	t.Parallel()

	items := menu(&menuState{
		version:      "v1.2.3",
		status:       "Waiting for MSFS",
		destinations: []string{"UDP: 127.0.0.1", "NMEA: COM11 at 115200"},
		msfs:         exexml.Ours,
		canStart:     true,
	})

	var ids []int

	texts := make([]string, 0, len(items))

	for _, item := range items {
		if item.id != 0 {
			ids = append(ids, item.id)
		}

		if item.checked && item.id != idMSFS {
			t.Errorf("%q is checked", item.text)
		}

		texts = append(texts, item.text)
	}

	if !slices.Equal(ids, []int{idMSFS, idLog, idQuit}) {
		t.Errorf("command IDs %v", ids)
	}

	want := []string{
		"SkyDemonConnect v1.2.3", "Waiting for MSFS", "UDP: 127.0.0.1", "NMEA: COM11 at 115200",
		"", startText, "Open log", "", "Quit",
	}
	if !slices.Equal(texts, want) {
		t.Errorf("menu %q, want %q", texts, want)
	}
}

func TestMenuStartWithMSFS(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		msfs     exexml.State
		canStart bool
		text     string
		id       int
		checked  bool
	}{
		{exexml.Ours, true, startText, idMSFS, true},
		{exexml.Absent, true, startText, idMSFS, false},
		{exexml.Other, true, otherText, idMSFS, false}, // a click replaces it
		{exexml.Ours, false, noExeText, 0, false},      // disabled
	} {
		items := menu(&menuState{msfs: tc.msfs, canStart: tc.canStart})

		i := slices.IndexFunc(
			items,
			func(m menuItem) bool { return m.id == idMSFS || m.text == noExeText },
		)
		if i < 0 {
			t.Fatalf("%v: no Start with MSFS item", tc.msfs)
		}

		if got := items[i]; got.text != tc.text || got.id != tc.id || got.checked != tc.checked {
			t.Errorf("%v, canStart %v: %+v", tc.msfs, tc.canStart, got)
		}
	}
}

func TestAddedMessage(t *testing.T) {
	t.Parallel()

	if got := addedMessage(nil); got != "Options: none. "+addedText {
		t.Errorf("without options: %q", got)
	}

	got := addedMessage([]string{"-udp=auto,192.168.1.23:49002", "-com=COM11"})
	if !strings.HasPrefix(got, "Options: -udp=auto,192.168.1.23:49002 -com=COM11. ") {
		t.Errorf("with options: %q", got)
	}
}
