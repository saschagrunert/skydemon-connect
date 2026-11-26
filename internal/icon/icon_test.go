package icon_test

import (
	"testing"

	"github.com/saschagrunert/skydemon-connect/internal/icon"
)

func TestRGB(t *testing.T) {
	t.Parallel()

	if c := icon.Green; c.R() != 0x2E || c.G() != 0x9E || c.B() != 0x4F {
		t.Errorf("green: %x %x %x", c.R(), c.G(), c.B())
	}
}

func TestRender(t *testing.T) {
	t.Parallel()

	for _, size := range []int{16, 20, 24, 32, 48, 256} {
		img := icon.Render(size, icon.Green)
		if b := img.Bounds(); b.Dx() != size || b.Dy() != size {
			t.Fatalf("%d: bounds %v", size, b)
		}

		if img.NRGBAAt(0, 0).A != 0 {
			t.Errorf("%d: corner not transparent", size)
		}

		if c := img.NRGBAAt(size/2, size/2); c.R < 200 || c.G < 200 || c.B < 200 || c.A != 255 {
			t.Errorf("%d: centre should be the white arrow, got %v", size, c)
		}

		// Below the arrow's notch the disc colour shows.
		if c := img.NRGBAAt(size/2, size*7/8); c.A < 200 || c.G < 0x80 || c.R > 0x60 {
			t.Errorf("%d: disc should be green, got %v", size, c)
		}
	}
}
