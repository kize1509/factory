package art

import "sort"

// The Aegean map shows every job at once: each project is a country, each
// of its jobs a city-state on one of the country's islands, and boats carry
// Assembly messages between them. It is bigger than the screen; you drag and
// zoom around it like the city.

const MapW, MapH = 3000, 1800

// The islands sit in the middle with open sea all round, so they never touch
// the edge of the screen. sea moves them there; toShore stretches the mainland
// (drawn for a 2400×1350 map) out to the edges.
var sea = Pt{415, 250}

func toShore(p Pt) Pt { return Pt{p.X * MapW / 2400, p.Y * 4 / 3} }

// Country is a group of islands that one project holds. Border is the sea
// boundary drawn around them on the map.
type Country struct {
	Name   string `json:"name"`
	Slots  []int  `json:"slots"`
	Border []Pt   `json:"border"`
}

// Countries come in the order projects take them. Each has three islands.
var Countries = []Country{{Name: "Attica"}, {Name: "Ionia"}, {Name: "Cyclades"}, {Name: "Dodecanese"}}

var countryAt = []Pt{{640, 430}, {1750, 430}, {640, 1000}, {1750, 1000}}

// MapSlots are the island centres where city-states stand: three per
// country, country by country.
var MapSlots []Pt

func init() {
	for c, at := range countryAt {
		at = Pt{at.X + sea.X, at.Y + sea.Y}
		for _, d := range []Pt{{-230, -80}, {230, -50}, {0, 160}} {
			Countries[c].Slots = append(Countries[c].Slots, len(MapSlots))
			MapSlots = append(MapSlots, Pt{at.X + d.X, at.Y + d.Y})
		}
	}
	for c := range Countries {
		var pts []Pt
		for _, i := range Countries[c].Slots {
			rx, ry := islandR(i)*138/100+70, islandR(i)*95/100+60
			for a := 0; a < 48; a++ {
				x, y := ringPt(a, 100)
				pts = append(pts, Pt{MapSlots[i].X + x*rx/100, MapSlots[i].Y + 6 + y*ry/100})
			}
		}
		Countries[c].Border = hull(pts)
	}
}

func islandR(i int) int { return 80 + (i*11)%24 }

// hull is the convex hull of pts, counter-clockwise (monotone chain).
func hull(pts []Pt) []Pt {
	sort.Slice(pts, func(a, b int) bool {
		return pts[a].X < pts[b].X || pts[a].X == pts[b].X && pts[a].Y < pts[b].Y
	})
	cross := func(o, a, b Pt) int { return (a.X-o.X)*(b.Y-o.Y) - (a.Y-o.Y)*(b.X-o.X) }
	var h []Pt
	for pass := 0; pass < 2; pass++ {
		start := len(h)
		for _, p := range pts {
			for len(h) >= start+2 && cross(h[len(h)-2], h[len(h)-1], p) <= 0 {
				h = h[:len(h)-1]
			}
			h = append(h, p)
		}
		h = h[:len(h)-1]
		for i, j := 0, len(pts)-1; i < j; i, j = i+1, j-1 {
			pts[i], pts[j] = pts[j], pts[i]
		}
	}
	return h
}

// AegeanMap draws the mainland, the sea with shallows around every coast,
// one island per slot, smaller islets and a compass rose.
func AegeanMap() *Canvas {
	c := New(MapW, MapH)
	c.Gradient(0, 0, MapW, MapH, Sea, SeaDeep)
	for y := 6; y < MapH; y += 9 {
		for x := (y*97 + y*y) % 41; x < MapW; x += 37 + (x*7+y)%23 {
			c.HLine(x, x+4, y, Foam)
		}
	}
	// mainland along the top and the west edge, with mountains and forest
	coast := []Pt{{0, 0}, {2400, 0}, {2400, 90}, {2220, 130}, {2040, 96}, {1830, 128}, {1620, 88}, {1410, 124}, {1230, 84}, {1020, 116}, {810, 86}, {630, 126}, {450, 104}, {300, 160}, {180, 150}, {120, 280}, {60, 450}, {90, 620}, {30, 790}, {0, 840}}
	for i := range coast {
		coast[i] = toShore(coast[i])
	}
	mainland(c, coast)
	for _, m := range [][3]int{{360, 40, 30}, {450, 54, 20}, {930, 40, 26}, {1500, 32, 28}, {1590, 48, 18}, {2130, 40, 24}, {50, 220, 20}, {40, 400, 16}} {
		at := toShore(Pt{m[0], m[1]})
		x, y, r := at.X, at.Y, m[2]
		c.Poly([]Pt{{x - r, y + r}, {x, y - r/2}, {x + r, y + r}}, Stone)
		c.Poly([]Pt{{x, y - r/2}, {x + r, y + r}, {x + r/3, y + r}}, StoneDark)
		c.Poly([]Pt{{x - r/4, y - r/8}, {x, y - r/2}, {x + r/4, y - r/8}}, Marble)
	}
	for i := 0; i < 60; i++ {
		at := toShore(Pt{180 + (i*173)%2070, 50 + (i*37)%50})
		x, y := at.X, at.Y
		if c.Get(x, y) == Olive || c.Get(x, y) == OliveDark {
			OliveTree(c, x, y)
		}
	}
	// islands for cities, each a different shape
	for i, s := range MapSlots {
		r := islandR(i)
		land(c, blob(s, r, 13, 9, i))
		if i%2 == 0 { // a hill on some islands
			c.Ellipse(s.X+r/2, s.Y-r/4, 16, 9, Olive)
			c.DitherOn(s.X+r/2-16, s.Y-r/4-9, 32, 18, Olive, GrassLight, 0.3)
		}
	}
	// uninhabited islets
	for i, s := range []Pt{{1200, 720}, {1200, 300}, {1200, 1180}, {2300, 720}, {300, 1250}, {2280, 1260}} {
		s = Pt{s.X + sea.X, s.Y + sea.Y}
		land(c, blob(s, 22+(i*7)%16, 13, 9, i+3))
	}
	// reefs
	for _, r := range [][2]int{{1000, 720}, {1400, 640}, {250, 700}, {1000, 1290}, {2200, 1040}, {2300, 450}} {
		r = [2]int{r[0] + sea.X, r[1] + sea.Y}
		c.Ellipse(r[0], r[1], 6, 3, StoneDark)
		c.Ellipse(r[0]-1, r[1]-1, 4, 2, Stone)
		c.HLine(r[0]-9, r[0]+9, r[1]+3, Foam)
	}
	compass(c, 200, 1700)
	return c
}

// blob is a rounded, slightly uneven island outline around p.
func blob(p Pt, r, sx, sy, seed int) []Pt {
	var pts []Pt
	for a := 0; a < 48; a++ {
		x, y := ringPt(a, 100)
		wob := 100 + []int{0, 3, 5, 6, 5, 3, 0, -3, -5, -6, -5, -3}[(a+seed*5)%12] + []int{0, 2, 0, -2}[(a/3+seed)%4]
		pts = append(pts, Pt{p.X + x*r*wob*sx/100000, p.Y + y*r*wob*sy/100000 + 6})
	}
	return pts
}

// mainland draws the coast along the top and west edges with even bands:
// shallows, foam, beach, then grass. Each band moves the coastline out to
// sea by a fixed amount, which a shape scaled about its centre can't do.
func mainland(c *Canvas, coast []Pt) {
	shift := func(d int) []Pt {
		out := make([]Pt, len(coast))
		for i, p := range coast {
			switch {
			case p.Y == 0: // the map's top edge stays put
				out[i] = p
			case p.X <= 260: // the west coast faces east
				out[i] = Pt{p.X + d, p.Y + d/2}
			default: // the north coast faces south
				out[i] = Pt{p.X, p.Y + d}
			}
		}
		return out
	}
	c.Poly(shift(26), Sea)
	c.Poly(shift(8), Foam)
	c.Poly(coast, Sand)
	c.Poly(shift(-22), Olive)
	c.DitherOn(0, 0, MapW, 340, Olive, OliveDark, 0.22)
	c.DitherOn(0, 0, 500, MapH, Olive, OliveDark, 0.22)
}

// land fills a shape as land: lighter shallows and foam around it, then a
// beach, grass and a few trees.
func land(c *Canvas, pts []Pt) {
	cx, cy := 0, 0
	for _, p := range pts {
		cx, cy = cx+p.X, cy+p.Y
	}
	cx, cy = cx/len(pts), cy/len(pts)
	scale := func(n, d int) []Pt {
		out := make([]Pt, len(pts))
		for i, p := range pts {
			out[i] = Pt{cx + (p.X-cx)*n/d, cy + (p.Y-cy)*n/d}
		}
		return out
	}
	shallow := scale(118, 100)
	c.Poly(shallow, Sea)
	minX, minY, maxX, maxY := shallow[0].X, shallow[0].Y, shallow[0].X, shallow[0].Y
	for _, p := range shallow {
		minX, minY, maxX, maxY = min(minX, p.X), min(minY, p.Y), max(maxX, p.X), max(maxY, p.Y)
	}
	c.DitherOn(minX, minY, maxX-minX, maxY-minY, Sea, Foam, 0.12)
	c.Poly(scale(106, 100), Foam)
	c.Poly(pts, Sand)
	inner := scale(84, 100)
	c.Poly(inner, Olive)
	c.DitherOn(minX, minY, maxX-minX, maxY-minY, Olive, OliveDark, 0.22)
	for i := 0; i < 8; i++ {
		x, y := minX+((i*53+17)%max(1, maxX-minX)), minY+((i*31+11)%max(1, maxY-minY))
		if g := c.Get(x, y); g == Olive || g == OliveDark {
			OliveTree(c, x, y)
		}
	}
}

// compass draws a small compass rose.
func compass(c *Canvas, x, y int) {
	c.Circle(x, y, 22, SeaDeep)
	c.Circle(x, y, 20, Sea)
	for a := 0; a < 48; a += 2 {
		px, py := ringPt(a, 20)
		c.Set(x+px, y+py, Marble)
	}
	c.Poly([]Pt{{x, y - 18}, {x + 4, y}, {x - 4, y}}, Marble)
	c.Poly([]Pt{{x, y + 18}, {x + 4, y}, {x - 4, y}}, MarbleShade)
	c.Poly([]Pt{{x - 18, y}, {x, y - 4}, {x, y + 4}}, MarbleShade)
	c.Poly([]Pt{{x + 18, y}, {x, y - 4}, {x, y + 4}}, MarbleShade)
	c.Poly([]Pt{{x, y - 18}, {x + 4, y}, {x, y}}, Fail)
	c.Set(x, y, Bronze)
	// N
	c.VLine(x-2, y-30, y-25, Marble)
	c.VLine(x+2, y-30, y-25, Marble)
	c.Line(x-2, y-30, x+2, y-25, Marble)
}

// Polis is one city-state seen from far away. Working cities fly a waving
// banner, finished ones wear a laurel wreath, empty slots show a bare hill.
func Polis(state string, f int) *Canvas {
	c := New(64, 56)
	if state == "empty" {
		c.Ellipse(32, 44, 14, 5, OliveDark)
		c.VLine(32, 28, 44, InkSoft)
		c.Rect(33, 28, 8, 5, MarbleShade)
		return finish(c)
	}
	c.Ellipse(32, 44, 28, 10, StoneDark)
	c.Ellipse(32, 42, 26, 8, Stone)
	c.Ellipse(32, 42, 22, 6, Sand)
	for i, h := range [][2]int{{14, 38}, {22, 42}, {40, 42}, {48, 38}, {30, 46}} {
		c.Rect(h[0], h[1], 7, 5, Marble)
		c.HLine(h[0]-1, h[0]+7, h[1]-1, []uint8{Terracotta, TerracottaDark}[i%2])
	}
	c.Rect(22, 24, 20, 12, Marble)
	for x := 23; x < 42; x += 4 {
		c.VLine(x, 26, 35, MarbleShade)
	}
	c.Poly([]Pt{{20, 24}, {32, 16}, {44, 24}}, Marble)
	c.Line(20, 24, 32, 16, Terracotta)
	c.Line(32, 16, 44, 24, Terracotta)
	c.HLine(20, 44, 36, Stone)
	finish(c)
	switch state {
	case "active":
		c.VLine(46, 4, 18, InkSoft)
		w := []int{0, 1}[f]
		c.Rect(47, 4+w, 9, 5, Terracotta)
		c.Set(55, 5+w, TerracottaDark)
		c.Set(55, 8+w, TerracottaDark)
	case "done":
		for a := 0; a < 48; a++ {
			if a > 18 && a < 30 {
				continue
			}
			x, y := ringPt(a, 9)
			c.Set(32+x, 9+y, OK)
			if a%3 == 0 {
				c.Set(32+x*8/9, 9+y*8/9, Olive)
			}
		}
	}
	return c
}

// MapBoat is a small boat carrying an Assembly message, 2 frames.
func MapBoat() []*Canvas {
	var out []*Canvas
	for f := 0; f < 2; f++ {
		c := New(16, 12)
		c.Poly([]Pt{{1, 7}, {15, 7}, {12, 10}, {4, 10}}, InkSoft)
		c.VLine(8, 1, 7, InkSoft)
		c.Rect(4+f, 2, 7, 4, Marble)
		c.HLine(4+f, 10+f, 3, Terracotta)
		out = append(out, finish(c))
	}
	return out
}
