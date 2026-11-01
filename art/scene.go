package art

// World size in pixels. The city sits in the middle, with more to find
// around it. The app shows part of it and you move around.
const SceneW, SceneH = 1280, 720

// OX, OY is where the city's own 640x360 area sits in the world.
const OX, OY = 320, 200

const (
	horizon = OY + 150 // where the far sea meets the sky
	shore   = OY + 302 // top of the beach in front of the city
)

// Place is a spot in the world. Work places are where agents go; landmarks
// are things to discover. Front is where agent tokens gather.
type Place struct {
	ID    string `json:"id"`
	Asset string `json:"asset"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Step  string `json:"step"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	Front Pt     `json:"front"`
}

// work places, in city coordinates, back to front. Step is the place's
// number along the Sacred Way, the road the work follows from the Gates in
// the west to the Harbour in the east.
var workPlaces = []Place{
	{ID: "oracle", Asset: "bldg/oracle", Name: "Oracle of Delphi", Step: "6", X: 456, Y: 46},
	{ID: "council", Asset: "bldg/council", Name: "Council house", Step: "3", X: 178, Y: 124},
	{ID: "library", Asset: "bldg/library", Name: "Library", Step: "2", X: 84, Y: 140},
	{ID: "academy", Asset: "bldg/academy", Name: "Academy", Step: "4", X: 262, Y: 136},
	{ID: "theatre", Asset: "bldg/theatre", Name: "Theatre", X: 2, Y: 150},
	{ID: "symposium", Asset: "bldg/symposium", Name: "Symposium", Step: "4b", X: 250, Y: 214},
	{ID: "gates", Asset: "bldg/gates", Name: "The Gates", Step: "1", X: 8, Y: 236},
	{ID: "workshop", Asset: "bldg/workshop", Name: "Workshop", Step: "5", X: 352, Y: 228},
	{ID: "agora", Asset: "bldg/agora", Name: "Agora", X: 140, Y: 244},
	{ID: "areopagus", Asset: "bldg/areopagus", Name: "Areopagus", Step: "7", X: 448, Y: 240},
	{ID: "harbour", Asset: "bldg/harbour", Name: "Harbour", Step: "8", X: 530, Y: 262},
}

// landmarks, in world coordinates
var landmarks = []Place{
	{ID: "olympus", Asset: "bldg/olympus", Name: "Mount Olympus", X: 24, Y: 136},
	{ID: "horse", Asset: "bldg/horse", Name: "Wooden horse", X: 150, Y: 452},
	{ID: "poseidon", Asset: "bldg/poseidon", Name: "Temple of Poseidon", X: 1150, Y: 384},
	{ID: "pharos", Asset: "bldg/pharos", Name: "Lighthouse of Pharos", X: 168, Y: 556},
	{ID: "colossus", Asset: "bldg/colossus", Name: "Colossus", X: 1010, Y: 544},
}

// Layout is every place in the world, back to front.
var Layout []Place

func init() {
	Layout = append(Layout, landmarks[0])
	for _, p := range workPlaces {
		p.X, p.Y, p.Kind = p.X+OX, p.Y+OY, "work"
		Layout = append(Layout, p)
	}
	Layout = append(Layout, landmarks[1:]...)
	for i := range Layout {
		p := &Layout[i]
		if p.Kind == "" {
			p.Kind = "landmark"
		}
		w, h := buildingSize(p.Asset)
		p.Front = Pt{p.X + w/2, p.Y + h - 12}
	}
}

// city moves a point from city coordinates into the world.
func city(x, y int) Pt { return Pt{x + OX, y + OY} }

func cityPoly(pts ...[2]int) []Pt {
	out := make([]Pt, len(pts))
	for i, q := range pts {
		out[i] = city(q[0], q[1])
	}
	return out
}

// SceneBase draws the world: sky, far mountains and sea, the city hill,
// farms and forest to the west, vineyard and cape to the east, beach and
// near sea. Buildings, landmarks, water animation and tokens go on top.
func SceneBase() *Canvas {
	c := New(SceneW, SceneH)
	c.Gradient(0, 0, SceneW, 220, SkyHigh, Sky)
	c.Gradient(0, 220, SceneW, horizon-220, Sky, Haze)

	// far mountain range in the haze
	far := []Pt{{0, 300}, {70, 270}, {150, 286}, {250, 240}, {330, 262}, {420, 250}, {520, 280}, {640, 262}, {760, 286}, {900, 258}, {1010, 274}, {1120, 246}, {1200, 268}, {1280, 252}, {1280, horizon}, {0, horizon}}
	c.Poly(far, MarbleShade)
	for y := 240; y < horizon; y++ {
		c.Dither(0, y, SceneW, 1, Clear, Haze, float64(horizon-y)/float64(horizon-240)*0.6)
	}
	// far sea with glints and islands
	c.Rect(0, horizon, SceneW, 40, SeaDeep)
	for y := horizon + 2; y < horizon+40; y += 4 {
		for x := (y * 7) % 11; x < SceneW; x += 23 {
			c.HLine(x, x+3, y, Sea)
		}
	}
	for _, is := range [][3]int{{380, 40, 5}, {520, 22, 3}, {1060, 30, 4}, {1200, 18, 3}} {
		c.Ellipse(is[0], horizon+1, is[1], is[2], Stone)
		c.Ellipse(is[0]+4, horizon-1, is[1]/3, is[2]-1, MarbleShade)
	}
	c.Rect(0, horizon+2, SceneW, 3, SeaDeep)

	// Delphi mountain
	c.Poly(cityPoly([2]int{320, 200}, [2]int{390, 110}, [2]int{424, 98}, [2]int{460, 56}, [2]int{496, 22}, [2]int{526, 46}, [2]int{548, 74}, [2]int{580, 92}, [2]int{640, 140}, [2]int{700, 200}), Stone)
	c.Poly(cityPoly([2]int{496, 22}, [2]int{526, 46}, [2]int{548, 74}, [2]int{580, 92}, [2]int{640, 140}, [2]int{700, 200}, [2]int{520, 200}, [2]int{536, 120}, [2]int{506, 60}), StoneDark)
	c.Poly(cityPoly([2]int{482, 34}, [2]int{496, 22}, [2]int{510, 34}, [2]int{503, 39}, [2]int{496, 35}, [2]int{489, 40}), Marble)
	o := city(446, 114)
	c.Rect(o.X, o.Y, 96, 8, StoneDark)
	c.HLine(o.X, o.X+95, o.Y, Stone)
	// scrub on the lower slopes, only on the rock
	o = city(320, 150)
	c.DitherOn(o.X, o.Y, 380, 50, Stone, OliveDark, 0.3)
	c.DitherOn(o.X, o.Y, 380, 50, StoneDark, OliveDark, 0.2)

	// one long hill: west plain, the city, the east cape
	hill := append([]Pt{{0, 412}, {60, 404}, {160, 398}, {260, 396}},
		cityPoly([2]int{0, 188}, [2]int{80, 160}, [2]int{190, 130}, [2]int{300, 126}, [2]int{400, 146}, [2]int{480, 170}, [2]int{560, 196}, [2]int{640, 210})...)
	hill = append(hill, Pt{1040, 414}, Pt{1120, 420}, Pt{1200, 416}, Pt{1238, 432}, Pt{1250, shore}, Pt{0, shore})
	c.Poly(hill, Olive)
	c.Poly(cityPoly([2]int{0, 188}, [2]int{80, 160}, [2]int{190, 130}, [2]int{300, 126}, [2]int{400, 146}, [2]int{400, 153}, [2]int{300, 133}, [2]int{190, 137}, [2]int{80, 167}, [2]int{0, 195}), GrassLight)
	c.Poly([]Pt{{0, 412}, {60, 404}, {160, 398}, {260, 396}, {320, 388}, {320, 394}, {260, 402}, {160, 404}, {60, 410}, {0, 418}}, GrassLight)
	c.Dither(0, OY+250, SceneW, shore-OY-250, Olive, OliveDark, 0.3)
	// cliff face of the east cape
	c.Poly([]Pt{{1238, 432}, {1250, shore}, {1262, shore}, {1252, 440}}, StoneDark)

	// west: forest and striped fields
	for i := 0; i < 26; i++ {
		x, y := 8+(i*37)%150, 420+(i*13)%30
		if i%3 == 0 {
			c.Ellipse(x, y-8, 3, 9, OliveDark)
			c.VLine(x, y, y+2, InkSoft)
		} else {
			OliveTree(c, x, y)
		}
	}
	for r := 0; r < 6; r++ {
		y := 470 + r*5
		col := []uint8{Sand, Olive, GrassLight}[r%3]
		c.Rect(20+r*6, y, 150, 3, col)
		c.HLine(20+r*6, 169+r*6, y+3, OliveDark)
	}
	// east: vineyard rows
	for r := 0; r < 5; r++ {
		for x := 980 + r*4; x < 1120+r*4; x += 4 {
			c.Set(x, 440+r*7, OliveDark)
			c.Set(x+1, 439+r*7, Olive)
			if (x/4+r)%5 == 0 {
				c.Set(x, 441+r*7, ClothRed)
			}
		}
	}

	for _, t := range [][2]int{{100, 228}, {336, 210}, {434, 216}, {620, 236}, {240, 112}, {130, 120}, {330, 118}, {600, 300}, {214, 300}} {
		p := city(t[0], t[1])
		OliveTree(c, p.X, p.Y)
	}
	for _, t := range [][2]int{{980, 470}, {1010, 486}, {1180, 470}, {1210, 480}, {200, 440}, {280, 470}, {250, 494}} {
		OliveTree(c, t[0], t[1])
	}
	for _, x := range []int{76, 244, 344} {
		p := city(x, 200)
		c.Ellipse(p.X, p.Y, 3, 10, OliveDark)
		c.VLine(p.X, p.Y+9, p.Y+12, InkSoft)
	}

	road := func(width int, pts ...Pt) {
		for i := 0; i+1 < len(pts); i++ {
			a, b := pts[i], pts[i+1]
			for w := 0; w < width; w++ {
				c.Line(a.X, a.Y+w, b.X, b.Y+w, Sand)
			}
			c.Line(a.X, a.Y+width, b.X, b.Y+width, Stone)
		}
	}
	// the Sacred Way: Gates, Agora, Symposium, Workshop, Areopagus, Harbour
	road(4, Pt{120, 492}, city(-10, 293), city(40, 292), city(188, 294), city(290, 294), city(388, 293), city(480, 294), city(560, 292), Pt{960, 488}, Pt{1100, 480}, Pt{1186, 474})
	road(2, city(124, 196), city(150, 244))                                 // Library
	road(2, city(210, 172), city(190, 244))                                 // Council house
	road(2, city(298, 192), city(292, 214))                                 // Academy to Symposium
	road(2, city(400, 228), city(440, 170), city(472, 150), city(488, 118)) // stair up to Delphi
	road(2, city(42, 202), city(40, 236))                                   // Theatre
	road(2, city(480, 288), city(470, 240))                                 // Areopagus
	// milestones along the Sacred Way
	for _, x := range []int{60, 130, 240, 340, 430, 520} {
		p := city(x, 289)
		c.Rect(p.X, p.Y, 2, 4, Marble)
		c.Set(p.X, p.Y-1, MarbleShade)
	}

	c.Rect(0, shore, SceneW, 8, Sand)
	c.Dither(0, shore+6, SceneW, 2, Sand, Foam, 0.5)
	c.Rect(0, shore+8, SceneW, SceneH-shore-8, Sea)
	c.Gradient(0, shore+40, SceneW, SceneH-shore-40, Sea, SeaDeep)
	// rocks in the sea
	for _, r := range [][2]int{{420, 640}, {700, 600}, {880, 680}, {1220, 600}} {
		c.Ellipse(r[0], r[1], 6, 3, StoneDark)
		c.Ellipse(r[0]-1, r[1]-1, 4, 2, Stone)
		c.HLine(r[0]-8, r[0]+8, r[1]+3, Foam)
	}
	return c
}

// OliveTree draws a small olive tree with its trunk base at (x, y).
func OliveTree(c *Canvas, x, y int) {
	c.VLine(x, y-4, y, InkSoft)
	c.Ellipse(x, y-7, 4, 3, OliveDark)
	c.Ellipse(x-1, y-8, 3, 2, Olive)
	c.Set(x-2, y-9, GrassLight)
}

// Water is the wave loop laid over the near sea.
func Water() []*Canvas {
	var frames []*Canvas
	for f := 0; f < 6; f++ {
		c := New(SceneW, SceneH-shore-8)
		for y := 3; y < c.H; y += 6 {
			for x := (f*3 + y*5) % 17; x < SceneW; x += 17 {
				c.HLine(x, x+4, y, Foam)
				c.HLine(x+1, x+3, y-1, Sea)
			}
		}
		frames = append(frames, c)
	}
	return frames
}

// Stars twinkle at dusk only.
func Stars() []*Canvas {
	var frames []*Canvas
	for f := 0; f < 2; f++ {
		c := New(SceneW, 300)
		for i := 0; i < 260; i++ {
			x, y := (i*97+13)%SceneW, (i*53+7)%300
			if (i+f)%3 != 0 {
				c.Set(x, y, Marble)
			}
		}
		frames = append(frames, c)
	}
	return frames
}

// Cloud draws one of a few cloud shapes; they drift across the sky.
func Cloud(v int) *Canvas {
	c := New(64, 20)
	puffs := [][][3]int{
		{{14, 12, 9}, {28, 9, 11}, {42, 12, 8}, {52, 14, 6}},
		{{12, 13, 7}, {24, 10, 9}, {36, 12, 7}},
		{{16, 12, 8}, {30, 8, 10}, {46, 11, 9}},
	}[v]
	for _, p := range puffs {
		c.Ellipse(p[0], p[1]+2, p[2], p[2]/2+2, MarbleShade)
	}
	for _, p := range puffs {
		c.Ellipse(p[0], p[1], p[2], p[2]/2+1, Marble)
	}
	return c
}

// Birds is a small flock flapping, 2 frames.
func Birds() []*Canvas {
	var out []*Canvas
	for f := 0; f < 2; f++ {
		c := New(20, 10)
		for _, b := range [][2]int{{3, 4}, {10, 2}, {15, 6}} {
			x, y := b[0], b[1]
			if f == 0 {
				c.Set(x-1, y-1, Ink)
				c.Set(x+1, y-1, Ink)
			} else {
				c.Set(x-1, y+1, Ink)
				c.Set(x+1, y+1, Ink)
			}
			c.Set(x, y, Ink)
		}
		out = append(out, c)
	}
	return out
}

// UFO is the saucer that brings your messages to an agent, 4 frames of
// blinking lights.
func UFO() []*Canvas {
	var out []*Canvas
	lights := []uint8{FireHot, OK, Warn, Fail}
	for f := 0; f < 4; f++ {
		c := New(34, 18)
		c.Ellipse(17, 6, 7, 5, Foam)
		c.Ellipse(15, 4, 3, 2, Marble)
		c.Rect(9, 6, 17, 3, Foam)
		c.Ellipse(17, 10, 16, 4, Stone)
		c.Ellipse(17, 9, 15, 3, MarbleShade)
		c.HLine(4, 30, 11, StoneDark)
		c.Ellipse(17, 13, 6, 2, StoneDark)
		c.Outline(Ink)
		for i, x := range []int{6, 12, 17, 22, 28} {
			c.Set(x, 10, lights[(i+f)%4])
		}
		out = append(out, c)
	}
	return out
}

// Beam is the light the UFO shines down while it delivers a message.
func Beam() []*Canvas {
	var out []*Canvas
	for f := 0; f < 2; f++ {
		c := New(30, 72)
		for y := 0; y < c.H; y++ {
			half := 4 + y*11/c.H
			for x := 15 - half; x <= 15+half; x++ {
				if (x+y+f)%2 == 0 {
					c.Set(x, y, Foam)
				} else if (x+y*3+f)%7 == 0 {
					c.Set(x, y, Marble)
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// Notes float up from a singer, 6 frames.
func Notes() []*Canvas {
	var out []*Canvas
	for f := 0; f < 6; f++ {
		c := New(28, 30)
		for i, n := range [][2]int{{4, 26}, {14, 28}, {22, 24}} {
			y := (n[1] - f*4 - i*7 + 60) % 30
			x := n[0] + []int{0, 1, 0, -1}[(f+i)%4]
			col := []uint8{Ink, SeaDeep, Bronze}[i]
			c.Rect(x, y, 2, 2, col)
			c.VLine(x+2, y-4, y+1, col)
			c.Set(x+3, y-4, col)
		}
		out = append(out, c)
	}
	return out
}

// Scroll is a handoff note being carried to the next station, 2 frames.
func Scroll() []*Canvas {
	var out []*Canvas
	for f := 0; f < 2; f++ {
		c := New(16, 12)
		c.Rect(3, 3, 10, 6, Sand)
		c.HLine(4, 11, 5, InkSoft)
		c.HLine(4, 9, 7, InkSoft)
		c.Rect(1, 2+f, 3, 8-f, Marble)
		c.Rect(12, 3-f, 3, 8+f, Marble)
		c.VLine(2, 2+f, 9, MarbleShade)
		c.VLine(13, 3-f, 10, MarbleShade)
		c.Circle(8, 9, 1, Fail) // wax seal
		out = append(out, finish(c))
	}
	return out
}

// Decks is the DJ booth on the Theatre stage: two turntables and a mixer,
// records turning and lights blinking over 4 frames.
func Decks() []*Canvas {
	var out []*Canvas
	for f := 0; f < 4; f++ {
		c := New(48, 20)
		c.Rect(1, 6, 46, 13, InkSoft)
		c.Rect(1, 6, 46, 2, Stone)
		c.HLine(1, 46, 18, Ink)
		for _, cx := range []int{11, 37} {
			c.Circle(cx, 6, 7, StoneDark)
			c.Circle(cx, 6, 6, Ink)
			c.Circle(cx, 6, 2, []uint8{Fail, Sea}[cx/37])
			x, y := ringPt((f*12+cx)%48, 5)
			c.Set(cx+x, 6+y/2, MarbleShade) // the turning mark on the record
		}
		c.Rect(20, 2, 8, 9, Stone)
		for i, col := range []uint8{OK, Warn, Fail} {
			if (i+f)%3 != 0 {
				c.Set(22+i*2, 4, col)
			}
		}
		c.HLine(21, 26, 8, Ink)
		c.Set(21+(f*2)%6, 7, Marble) // crossfader
		for x := 4; x < 46; x += 6 { // lights along the front
			if (x/6+f)%2 == 0 {
				c.Set(x, 13, []uint8{Fail, OK, Sea, Warn}[(x/6+f)%4])
			}
		}
		out = append(out, finish(c))
	}
	return out
}
