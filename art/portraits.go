package art

// Philosopher describes one agent character. Portraits are drawn from these
// traits, so changing a look means changing a line here.
type Philosopher struct {
	ID, Name, Role string
	Hair           string // bald, short, curly, wild, receding, long
	HairCol        uint8
	Beard          string // none, short, full, long, scruffy
	BeardCol       uint8
	Cloth          uint8
	Band           uint8 // headband or laurel colour, Clear for none
	Prop           string
}

var Philosophers = []Philosopher{
	{"solon", "Solon", "scheduler", "short", HairGrey, "short", HairGrey, ClothBlue, Olive, "tablet"},
	{"herodotus", "Herodotus", "researcher", "curly", HairDark, "full", HairDark, Terracotta, Clear, "scroll"},
	{"thales", "Thales", "researcher", "receding", HairGrey, "long", HairGrey, ClothBlue, Clear, "star"},
	{"anaximander", "Anaximander", "researcher", "short", HairDark, "short", HairDark, Olive, Clear, "map"},
	{"eratosthenes", "Eratosthenes", "researcher", "short", InkSoft, "none", Clear, Sand, Clear, "staff"},
	{"aristotle", "Aristotle", "planner", "receding", InkSoft, "short", InkSoft, ClothRed, Clear, "book"},
	{"plato", "Plato", "planner", "short", HairGrey, "full", HairGrey, ClothBlue, Clear, "finger"},
	{"archimedes", "Archimedes", "builder", "wild", HairGrey, "full", HairGrey, Terracotta, Clear, "compass"},
	{"hero", "Hero", "builder", "short", HairDark, "none", Clear, ClothBlue, Clear, "sphere"},
	{"euclid", "Euclid", "builder", "short", InkSoft, "short", InkSoft, Olive, Clear, "square"},
	{"pythagoras", "Pythagoras", "builder", "short", HairDark, "full", HairDark, Marble, ClothRed, "triangle"},
	{"socrates", "Socrates", "reviewer", "bald", HairGrey, "full", HairGrey, MarbleShade, Clear, "none"},
	{"diogenes", "Diogenes", "reviewer", "wild", HairDark, "scruffy", HairDark, Stone, Clear, "lamp"},
	{"zeno", "Zeno", "reviewer", "short", HairDark, "short", HairDark, ClothBlue, Clear, "arrow"},
	{"heraclitus", "Heraclitus", "reviewer", "long", HairDark, "full", HairDark, ClothRed, Clear, "flame"},
	{"protagoras", "Protagoras", "humanizer", "curly", HairDark, "short", HairDark, ClothRed, Clear, "hand"},
	{"isocrates", "Isocrates", "humanizer", "receding", HairGrey, "long", HairGrey, Marble, Clear, "quill"},
	{"xenophon", "Xenophon", "finalizer", "helmet", Bronze, "short", HairDark, ClothBlue, Clear, "tablet"},
	// Hermes carries news between agents and runs the tracker: a winged cap
	// (the helmet look in bronze) and a staff.
	{"hermes", "Hermes", "ops", "helmet", Bronze, "none", Clear, ClothBlue, Clear, "staff"},
	// Homer narrates what is happening and why.
	{"homer", "Homer", "narrator", "long", HairGrey, "long", HairGrey, Marble, Olive, "none"},
	// DJs play sets at the Theatre.
	{"solomun", "Solomun", "dj", "short", HairDark, "short", HairDark, InkSoft, Clear, "headphones"},
}

// Portrait32 draws the 32x32 bust used in the drawer, the Inbox and the scene.
func Portrait32(p Philosopher) *Canvas { return portrait(p, 0) }

// PortraitTalk is the bust speaking: mouth closed, open, wide, open.
func PortraitTalk(p Philosopher) []*Canvas {
	return []*Canvas{portrait(p, 0), portrait(p, 1), portrait(p, 2), portrait(p, 1)}
}

// PortraitWave is the bust calling for you: mouth open, one hand up and
// waving over 4 frames.
func PortraitWave(p Philosopher) []*Canvas {
	var out []*Canvas
	for f := 0; f < 4; f++ {
		c := portrait(p, 1+f%2)
		dx := []int{0, 2, 0, -1}[f]
		// raised arm and open hand on the right side of the bust
		c.Line(22, 27, 24+dx/2, 15, p.Cloth)
		c.Line(23, 27, 25+dx/2, 15, p.Cloth)
		c.Rect(23+dx, 9, 5, 6, Skin)
		c.VLine(27+dx, 9, 14, SkinShade)
		for x := 23 + dx; x < 28+dx; x += 2 {
			c.Set(x, 8, Skin)
		}
		c.Set(22+dx, 11, Skin) // thumb
		c.ClipCircle(16, 16, 15)
		out = append(out, c)
	}
	return out
}

// portrait draws a bust; mouth is 0 closed, 1 open, 2 wide open.
func portrait(p Philosopher, mouth int) *Canvas {
	c := New(32, 32)
	c.Circle(16, 16, 15, Marble)
	c.Dither(0, 20, 32, 12, Marble, MarbleShade, 0.25)
	// shoulders and robe
	c.Ellipse(16, 31, 12, 7, p.Cloth)
	c.Line(9, 26, 15, 31, shade(p.Cloth))
	c.Rect(14, 20, 5, 5, Skin)
	c.VLine(18, 20, 24, SkinShade)
	// head, turned slightly to the left
	c.Ellipse(15, 13, 6, 7, Skin)
	c.VLine(20, 10, 16, SkinShade)
	c.Set(21, 13, Skin) // ear
	c.Set(21, 14, SkinShade)
	hair32(c, p)
	beard32(c, p)
	// eyes, brow and nose
	c.Set(12, 13, Ink)
	c.Set(16, 13, Ink)
	c.HLine(11, 13, 11, shade(p.HairCol))
	c.HLine(15, 17, 11, shade(p.HairCol))
	c.Set(13, 15, SkinShade)
	c.Set(12, 16, SkinShade)
	switch {
	case mouth == 2:
		c.Rect(13, 17, 3, 3, Ink)
		c.Set(14, 19, ClothRed)
	case mouth == 1:
		c.Rect(13, 17, 3, 2, Ink)
	case p.Beard == "none":
		c.HLine(13, 15, 18, SkinShade)
	default:
		c.HLine(13, 15, 18, InkSoft)
	}
	if p.Band != Clear {
		c.HLine(9, 21, 8, p.Band)
		c.Set(10, 7, p.Band)
		c.Set(20, 7, p.Band)
	}
	prop32(c, p.Prop)
	c.ClipCircle(16, 16, 15)
	return c
}

func shade(p uint8) uint8 {
	switch p {
	case Marble:
		return MarbleShade
	case Terracotta:
		return TerracottaDark
	case Olive:
		return OliveDark
	case Stone, MarbleShade:
		return StoneDark
	case HairGrey:
		return Stone
	case Sand:
		return Bronze
	case Bronze:
		return InkSoft
	}
	return Ink
}

func hair32(c *Canvas, p Philosopher) {
	h := p.HairCol
	switch p.Hair {
	case "bald":
		c.Set(13, 7, Marble) // shine on the scalp
		c.Rect(9, 11, 2, 4, h)
		c.Rect(20, 11, 2, 3, h)
	case "short":
		c.Ellipse(15, 8, 7, 3, h)
		c.Rect(9, 8, 2, 5, h)
		c.Rect(20, 8, 2, 4, h)
	case "receding":
		c.Rect(9, 9, 2, 6, h)
		c.Rect(19, 8, 3, 6, h)
		c.HLine(15, 19, 6, h)
	case "curly":
		for _, q := range []Pt{{10, 7}, {13, 5}, {16, 5}, {19, 6}, {21, 9}, {9, 10}, {11, 6}, {18, 5}, {20, 7}} {
			c.Circle(q.X, q.Y, 1, h)
		}
		c.Rect(9, 9, 2, 5, h)
	case "wild":
		c.Ellipse(15, 8, 8, 3, h)
		for _, q := range []Pt{{7, 6}, {10, 3}, {14, 2}, {18, 3}, {22, 5}, {24, 9}, {6, 10}} {
			c.Line(15, 8, q.X, q.Y, h)
		}
		c.Rect(8, 8, 3, 7, h)
		c.Rect(20, 8, 3, 6, h)
	case "long":
		c.Ellipse(15, 8, 7, 3, h)
		c.Rect(8, 8, 3, 13, h)
		c.Rect(20, 8, 3, 13, h)
	case "helmet":
		c.Ellipse(15, 8, 8, 4, Bronze)
		c.HLine(7, 23, 10, BronzeLight)
		c.Rect(13, 1, 4, 4, ClothRed)
		c.Rect(9, 10, 2, 4, p.BeardCol)
	}
}

func beard32(c *Canvas, p Philosopher) {
	b := p.BeardCol
	switch p.Beard {
	case "short":
		c.Ellipse(15, 18, 5, 2, b)
	case "full":
		c.Ellipse(15, 18, 6, 4, b)
		c.Rect(9, 13, 2, 5, b)
		c.Rect(20, 14, 1, 4, b)
	case "long":
		c.Ellipse(15, 19, 6, 6, b)
		c.Rect(9, 13, 2, 6, b)
	case "scruffy":
		c.Ellipse(15, 18, 6, 4, b)
		for _, q := range []Pt{{9, 21}, {12, 23}, {16, 24}, {20, 21}, {8, 18}} {
			c.Set(q.X, q.Y, b)
		}
	}
	if p.Beard != "none" {
		c.Ellipse(14, 17, 2, 1, Skin) // lips stay visible
	}
}

func prop32(c *Canvas, prop string) {
	switch prop {
	case "tablet":
		c.Rect(21, 22, 8, 7, Bronze)
		c.Rect(22, 23, 6, 5, Sand)
		c.HLine(23, 26, 25, InkSoft)
	case "scroll":
		c.Rect(19, 24, 10, 4, Marble)
		c.VLine(19, 23, 28, Bronze)
		c.VLine(28, 23, 28, Bronze)
	case "star":
		c.Set(25, 4, FireHot)
		c.Set(24, 4, BronzeLight)
		c.Set(26, 4, BronzeLight)
		c.Set(25, 3, BronzeLight)
		c.Set(25, 5, BronzeLight)
	case "map":
		c.Circle(25, 25, 4, Sand)
		c.Rect(23, 24, 3, 2, Sea)
		c.Set(27, 26, Olive)
	case "staff":
		c.Line(26, 4, 26, 30, Bronze)
		c.Set(26, 3, BronzeLight)
	case "book":
		c.Rect(20, 22, 8, 8, ClothRed)
		c.VLine(27, 22, 29, Marble)
	case "finger":
		c.Rect(23, 14, 2, 10, Skin)
		c.VLine(24, 14, 23, SkinShade)
	case "compass":
		c.Line(25, 20, 21, 30, Ink)
		c.Line(25, 20, 29, 30, Ink)
		c.Set(25, 19, BronzeLight)
	case "sphere":
		c.Circle(25, 25, 3, Bronze)
		c.Set(24, 24, BronzeLight)
		c.Set(29, 23, Smoke)
		c.Set(21, 27, Smoke)
	case "square":
		c.VLine(22, 21, 30, Bronze)
		c.HLine(22, 29, 30, Bronze)
	case "triangle":
		c.Line(21, 30, 25, 22, Bronze)
		c.Line(25, 22, 29, 30, Bronze)
		c.HLine(21, 29, 30, Bronze)
	case "lamp":
		c.Ellipse(25, 26, 4, 2, Bronze)
		c.Set(29, 25, Bronze)
		c.Set(22, 24, Fire)
		c.Set(22, 23, FireHot)
	case "arrow":
		c.HLine(19, 29, 25, Ink)
		c.Line(29, 25, 27, 23, Ink)
		c.Line(29, 25, 27, 27, Ink)
	case "flame":
		c.Ellipse(26, 25, 2, 3, Fire)
		c.Set(26, 25, FireHot)
		c.Set(26, 21, Fire)
	case "hand":
		c.Rect(23, 18, 4, 5, Skin)
		c.VLine(23, 15, 18, Skin)
		c.VLine(25, 14, 18, Skin)
		c.VLine(26, 18, 22, SkinShade)
	case "lyre":
		c.Line(20, 30, 21, 19, Bronze)
		c.Line(29, 30, 28, 19, Bronze)
		c.Set(20, 18, BronzeLight)
		c.Set(29, 18, BronzeLight)
		c.HLine(21, 28, 21, Bronze)
		c.HLine(20, 29, 30, Bronze)
		for x := 22; x <= 27; x += 2 {
			c.VLine(x, 22, 29, Marble)
		}
	case "headphones":
		c.Line(8, 12, 10, 5, Ink)
		c.Line(10, 5, 15, 3, Ink)
		c.Line(15, 3, 20, 5, Ink)
		c.Line(20, 5, 22, 12, Ink)
		c.Rect(6, 11, 4, 6, Ink)
		c.Rect(20, 11, 4, 6, Ink)
		c.VLine(7, 12, 15, ClothRed)
		c.VLine(22, 12, 15, ClothRed)
	case "quill":
		c.Line(21, 30, 28, 18, Marble)
		c.Line(22, 30, 28, 19, MarbleShade)
		c.Set(21, 31, Ink)
	}
}

// Token16 is the small version of a portrait that stands in the scene.
func Token16(p Philosopher) *Canvas {
	c := New(16, 16)
	c.Circle(8, 8, 7, Marble)
	c.Ellipse(8, 15, 6, 3, p.Cloth)
	c.Ellipse(8, 7, 3, 4, Skin)
	c.VLine(10, 5, 9, SkinShade)
	switch p.Hair {
	case "bald":
		c.Set(5, 6, p.HairCol)
		c.Set(11, 6, p.HairCol)
	case "helmet":
		c.HLine(4, 11, 3, Bronze)
		c.HLine(5, 10, 2, Bronze)
		c.Set(7, 1, ClothRed)
		c.Set(8, 1, ClothRed)
	case "wild":
		c.HLine(4, 12, 3, p.HairCol)
		c.Set(3, 2, p.HairCol)
		c.Set(8, 1, p.HairCol)
		c.Set(12, 2, p.HairCol)
	case "long":
		c.HLine(5, 11, 3, p.HairCol)
		c.VLine(4, 4, 10, p.HairCol)
		c.VLine(11, 4, 10, p.HairCol)
	case "receding":
		c.VLine(5, 4, 7, p.HairCol)
		c.VLine(11, 4, 7, p.HairCol)
	default:
		c.HLine(5, 11, 3, p.HairCol)
		c.Set(5, 4, p.HairCol)
		c.Set(11, 4, p.HairCol)
	}
	switch p.Beard {
	case "full", "scruffy":
		c.Ellipse(8, 10, 3, 2, p.BeardCol)
	case "long":
		c.Ellipse(8, 11, 3, 3, p.BeardCol)
	case "short":
		c.HLine(6, 10, 10, p.BeardCol)
	}
	c.Set(7, 7, Ink)
	c.Set(9, 7, Ink)
	if p.Band != Clear {
		c.HLine(5, 11, 3, p.Band)
	}
	c.ClipCircle(8, 8, 7)
	return c
}

// Frame is the bronze rim drawn around a portrait of radius r.
func Frame(r int) *Canvas {
	s := 2*r + 6
	c := New(s, s)
	m := s / 2
	c.Circle(m, m, r+2, Bronze)
	c.Circle(m, m, r, Clear)
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			dx, dy := x-m, y-m
			d := dx*dx + dy*dy
			if d <= r*r+r {
				c.Erase(x, y)
			} else if d <= (r+1)*(r+1) && (x+y)%3 == 0 {
				c.Set(x, y, BronzeLight)
			}
		}
	}
	c.Outline(Ink)
	return c
}

// Ring is a status ring of radius r around a scene token. Rotating rings
// get several frames.
func Ring(col uint8, dotted bool, n, r int) []*Canvas {
	var out []*Canvas
	for f := 0; f < n; f++ {
		s := 2*r + 4
		c := New(s, s)
		for a := 0; a < 48; a++ {
			x, y := ringPt(a, r)
			if dotted && (a*4*n/48+f)%n < n/4 {
				continue
			}
			if !dotted && n > 1 && f%2 == 1 && a%2 == 0 {
				continue
			}
			c.Set(s/2+x, s/2+y, col)
			c.Set(s/2+x*(r-1)/r, s/2+y*(r-1)/r, col)
		}
		out = append(out, c)
	}
	return out
}

// ringPt walks a circle in 48 steps using a small integer table.
func ringPt(a, r int) (int, int) {
	cos := []int{100, 99, 97, 92, 87, 79, 71, 61, 50, 38, 26, 13, 0}
	q, i := a/12, a%12
	x, y := cos[i]*r/100, cos[12-i]*r/100
	switch q {
	case 1:
		x, y = -y, x
	case 2:
		x, y = -x, -y
	case 3:
		x, y = y, -x
	}
	return x, y
}
