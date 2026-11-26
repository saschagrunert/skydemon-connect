package tray

import (
	"strings"
	"testing"

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

	items := menu("v1.2.3", "Waiting for MSFS", true)

	var ids []int

	for _, item := range items {
		if item.id != 0 {
			ids = append(ids, item.id)
		}

		if item.checked && item.id != idMSFS {
			t.Errorf("%q is checked", item.text)
		}
	}

	if len(ids) != 3 || ids[0] != idMSFS || ids[1] != idLog || ids[2] != idQuit {
		t.Errorf("command IDs %v", ids)
	}

	if items[0].text != "SkyDemonConnect v1.2.3" || items[1].text != "Waiting for MSFS" ||
		!items[3].checked {
		t.Errorf("menu %+v", items)
	}

	if menu("v1", "", false)[3].checked {
		t.Error("Start with MSFS checked while off")
	}
}
