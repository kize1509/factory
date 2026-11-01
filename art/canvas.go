package art

import "image"

// Canvas is a palette-indexed image. A pixel value of Clear is transparent.
type Canvas struct {
	W, H int
	Pix  []uint8
}

type Pt struct {
	X int `json:"x"`
	Y int `json:"y"`
}

func New(w, h int) *Canvas { return &Canvas{w, h, make([]uint8, w*h)} }

func (c *Canvas) in(x, y int) bool { return x >= 0 && y >= 0 && x < c.W && y < c.H }

// Set paints one pixel. Painting Clear does nothing; use Erase for that.
func (c *Canvas) Set(x, y int, p uint8) {
	if p != Clear && c.in(x, y) {
		c.Pix[y*c.W+x] = p
	}
}

func (c *Canvas) Erase(x, y int) {
	if c.in(x, y) {
		c.Pix[y*c.W+x] = Clear
	}
}

func (c *Canvas) Get(x, y int) uint8 {
	if !c.in(x, y) {
		return Clear
	}
	return c.Pix[y*c.W+x]
}

func (c *Canvas) Rect(x, y, w, h int, p uint8) {
	for j := y; j < y+h; j++ {
		for i := x; i < x+w; i++ {
			c.Set(i, j, p)
		}
	}
}

func (c *Canvas) HLine(x0, x1, y int, p uint8) { c.Rect(x0, y, x1-x0+1, 1, p) }
func (c *Canvas) VLine(x, y0, y1 int, p uint8) { c.Rect(x, y0, 1, y1-y0+1, p) }

func (c *Canvas) Line(x0, y0, x1, y1 int, p uint8) {
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := sign(x1-x0), sign(y1-y0)
	e := dx + dy
	for {
		c.Set(x0, y0, p)
		if x0 == x1 && y0 == y1 {
			return
		}
		if e2 := 2 * e; e2 >= dy {
			e += dy
			x0 += sx
		} else {
			e += dx
			y0 += sy
		}
	}
}

func (c *Canvas) Ellipse(cx, cy, rx, ry int, p uint8) {
	for y := -ry; y <= ry; y++ {
		for x := -rx; x <= rx; x++ {
			if x*x*ry*ry+y*y*rx*rx <= rx*rx*ry*ry+(rx*ry) {
				c.Set(cx+x, cy+y, p)
			}
		}
	}
}

func (c *Canvas) Circle(cx, cy, r int, p uint8) { c.Ellipse(cx, cy, r, r, p) }

// Poly fills a polygon with the even-odd rule.
func (c *Canvas) Poly(pts []Pt, p uint8) {
	minY, maxY := pts[0].Y, pts[0].Y
	for _, q := range pts {
		minY, maxY = min(minY, q.Y), max(maxY, q.Y)
	}
	for y := minY; y <= maxY; y++ {
		var xs []int
		for i := range pts {
			a, b := pts[i], pts[(i+1)%len(pts)]
			if (a.Y <= y && b.Y > y) || (b.Y <= y && a.Y > y) {
				xs = append(xs, a.X+(y-a.Y)*(b.X-a.X)/(b.Y-a.Y))
			}
		}
		for i := 1; i < len(xs); i++ {
			for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
				xs[j], xs[j-1] = xs[j-1], xs[j]
			}
		}
		for i := 0; i+1 < len(xs); i += 2 {
			c.HLine(xs[i], xs[i+1], y, p)
		}
	}
}

var bayer = [4][4]int{{0, 8, 2, 10}, {12, 4, 14, 6}, {3, 11, 1, 9}, {15, 7, 13, 5}}

// Dither fills a rectangle mixing a and b; t is the share of b (0..1).
func (c *Canvas) Dither(x, y, w, h int, a, b uint8, t float64) {
	for j := y; j < y+h; j++ {
		for i := x; i < x+w; i++ {
			if float64(bayer[j&3][i&3]) < t*16 {
				c.Set(i, j, b)
			} else {
				c.Set(i, j, a)
			}
		}
	}
}

// DitherOn mixes b into pixels that are currently on, leaving everything
// else alone, so texture follows a shape instead of a rectangle.
func (c *Canvas) DitherOn(x, y, w, h int, on, b uint8, t float64) {
	for j := y; j < y+h; j++ {
		for i := x; i < x+w; i++ {
			if c.Get(i, j) == on && float64(bayer[j&3][i&3]) < t*16 {
				c.Set(i, j, b)
			}
		}
	}
}

// Gradient fills a rectangle going from a at the top to b at the bottom.
func (c *Canvas) Gradient(x, y, w, h int, a, b uint8) {
	for j := 0; j < h; j++ {
		c.Dither(x, y+j, w, 1, a, b, float64(j)/float64(h-1))
	}
}

// Blit copies the opaque pixels of src onto c.
func (c *Canvas) Blit(src *Canvas, dx, dy int) {
	for y := 0; y < src.H; y++ {
		for x := 0; x < src.W; x++ {
			c.Set(dx+x, dy+y, src.Get(x, y))
		}
	}
}

// Outline draws p on every clear pixel that touches an opaque one.
func (c *Canvas) Outline(p uint8) {
	o := c.Clone()
	for y := 0; y < c.H; y++ {
		for x := 0; x < c.W; x++ {
			if o.Get(x, y) != Clear {
				continue
			}
			if o.Get(x-1, y) != Clear || o.Get(x+1, y) != Clear || o.Get(x, y-1) != Clear || o.Get(x, y+1) != Clear {
				c.Set(x, y, p)
			}
		}
	}
}

func (c *Canvas) Clone() *Canvas {
	n := New(c.W, c.H)
	copy(n.Pix, c.Pix)
	return n
}

// Replace swaps one palette index for another.
func (c *Canvas) Replace(from, to uint8) {
	for i, p := range c.Pix {
		if p == from {
			c.Pix[i] = to
		}
	}
}

// ClipCircle erases everything outside a circle.
func (c *Canvas) ClipCircle(cx, cy, r int) {
	for y := 0; y < c.H; y++ {
		for x := 0; x < c.W; x++ {
			if dx, dy := x-cx, y-cy; dx*dx+dy*dy > r*r+r {
				c.Erase(x, y)
			}
		}
	}
}

// Scale enlarges a canvas by a whole number, each pixel becoming an n×n block.
func Scale(c *Canvas, n int) *Canvas {
	out := New(c.W*n, c.H*n)
	for y := 0; y < out.H; y++ {
		for x := 0; x < out.W; x++ {
			out.Pix[y*out.W+x] = c.Pix[(y/n)*c.W+x/n]
		}
	}
	return out
}

// Strip lays frames side by side, left to right.
func Strip(frames ...*Canvas) *Canvas {
	s := New(frames[0].W*len(frames), frames[0].H)
	for i, f := range frames {
		s.Blit(f, i*f.W, 0)
	}
	return s
}

// Image renders the canvas with the day or dusk palette.
func (c *Canvas) Image(dusk bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, c.W, c.H))
	for i, p := range c.Pix {
		col := Palette[p].Day
		if dusk {
			col = Palette[p].Dusk
		}
		img.SetNRGBA(i%c.W, i/c.W, col)
	}
	return img
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func sign(v int) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}
