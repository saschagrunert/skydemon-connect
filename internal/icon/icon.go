// Package icon draws the tray icon: a coloured disc with a white "ownship"
// arrow, antialiased, at any size.
package icon

import (
	"image"
	"math"
)

const (
	supersample  = 4    // samples per pixel and axis
	discRadius   = 0.96 // in unit coordinates
	channelMax   = 255
	roundingBias = 0.5
	redShift     = 16
	greenShift   = 8
)

// RGB is a colour as 0xRRGGBB.
type RGB uint32

// The colours of the app's states: sending, waiting and failed.
const (
	Green RGB = 0x2E9E4F
	Grey  RGB = 0x8A9099
	Red   RGB = 0xD23C3C
)

// R returns the red channel.
func (c RGB) R() uint8 { return uint8(c >> redShift & channelMax) }

// G returns the green channel.
func (c RGB) G() uint8 { return uint8(c >> greenShift & channelMax) }

// B returns the blue channel.
func (c RGB) B() uint8 { return uint8(c & channelMax) }

// arrow returns a navigation chevron in unit coordinates (-1..1, y down).
func arrow() [4][2]float64 {
	return [4][2]float64{{0, -0.62}, {0.46, 0.52}, {0, 0.26}, {-0.46, 0.52}}
}

func inPolygon(x, y float64, p [][2]float64) bool {
	inside := false

	for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
		xi, yi, xj, yj := p[i][0], p[i][1], p[j][0], p[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			inside = !inside
		}
	}

	return inside
}

// toUnit maps sub-sample sub of pixel p to unit coordinates (-1..1).
func toUnit(p, sub, size int) float64 {
	const span = 2 // from -1 to 1

	return (float64(p)+(float64(sub)+roundingBias)/supersample)/float64(size)*span - 1
}

// coverage returns the share of pixel (x, y) covered by the disc and the
// share of the disc's part that is arrow.
func coverage(x, y, size int) (float64, float64) {
	var inDisc, inArrow int

	a := arrow()

	for sy := range supersample {
		for sx := range supersample {
			u := toUnit(x, sx, size)
			v := toUnit(y, sy, size)

			if math.Hypot(u, v) > discRadius {
				continue
			}

			inDisc++

			if inPolygon(u, v, a[:]) {
				inArrow++
			}
		}
	}

	if inDisc == 0 {
		return 0, 0
	}

	return float64(inDisc) / (supersample * supersample), float64(inArrow) / float64(inDisc)
}

// Render returns the icon at size×size pixels, with straight alpha.
func Render(size int, c RGB) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))

	for y := range size {
		for x := range size {
			a, w := coverage(x, y, size)
			if a == 0 {
				continue
			}

			mix := func(ch uint8) uint8 { return uint8(float64(ch)*(1-w) + channelMax*w + roundingBias) }
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2] = mix(c.R()), mix(c.G()), mix(c.B())
			img.Pix[i+3] = uint8(a*channelMax + roundingBias)
		}
	}

	return img
}
