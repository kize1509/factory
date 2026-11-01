package art

// Building is one place in the scene. Each state is a list of frames;
// a state with one frame is still.
type Building struct {
	ID     string
	W, H   int
	States map[string][]*Canvas
}

var sizes = map[string][2]int{
	"bldg/gates":           {64, 56},
	"bldg/library":         {80, 56},
	"bldg/council":         {64, 48},
	"bldg/academy":         {72, 56},
	"bldg/workshop":        {72, 56},
	"bldg/oracle":          {64, 72},
	"bldg/areopagus":       {64, 48},
	"bldg/symposium":       {80, 56},
	"bldg/agora":           {96, 48},
	"bldg/harbour":         {96, 56},
	"bldg/theatre":         {80, 52},
	"bldg/trireme-docked":  {48, 24},
	"bldg/trireme-sailing": {48, 32},
}

func buildingSize(id string) (int, int) { s := sizes[id]; return s[0], s[1] }

// Buildings returns every building with all its states.
func Buildings() []Building {
	return []Building{
		{"bldg/gates", 64, 56, map[string][]*Canvas{"idle": {gates(false)}, "active": {gates(true)}}},
		{"bldg/library", 80, 56, map[string][]*Canvas{"idle": {library(false)}, "active": {library(true)}}},
		{"bldg/council", 64, 48, map[string][]*Canvas{"idle": {council(false, 0)}, "active": frames(3, func(f int) *Canvas { return council(true, f) })}},
		{"bldg/academy", 72, 56, map[string][]*Canvas{"idle": {academy(false)}, "active": {academy(true)}}},
		{"bldg/workshop", 72, 56, map[string][]*Canvas{"idle": {workshop(-1)}, "active": frames(4, workshop)}},
		{"bldg/oracle", 64, 72, map[string][]*Canvas{
			"idle":    {oracle(Smoke, 0, false)},
			"running": frames(4, func(f int) *Canvas { return oracle(Smoke, f, true) }),
			"pass":    frames(4, func(f int) *Canvas { return oracle(OK, f, true) }),
			"fail":    frames(4, func(f int) *Canvas { return oracle(Fail, f, true) }),
		}},
		{"bldg/areopagus", 64, 48, map[string][]*Canvas{"idle": {areopagus(false)}, "active": {areopagus(true)}}},
		{"bldg/symposium", 80, 56, map[string][]*Canvas{"idle": {symposium(-1)}, "active": frames(2, symposium)}},
		{"bldg/agora", 96, 48, map[string][]*Canvas{"idle": {agora()}}},
		{"bldg/harbour", 96, 56, map[string][]*Canvas{"idle": {harbour()}}},
		{"bldg/theatre", 80, 52, map[string][]*Canvas{"idle": {theatre(-1)}, "active": frames(2, theatre)}},
		{"bldg/trireme-docked", 48, 24, map[string][]*Canvas{"idle": {trireme(false, 0)}}},
		{"bldg/trireme-sailing", 48, 32, map[string][]*Canvas{"idle": frames(2, func(f int) *Canvas { return trireme(true, f) })}},
	}
}

func frames(n int, draw func(int) *Canvas) []*Canvas {
	out := make([]*Canvas, n)
	for i := range out {
		out[i] = draw(i)
	}
	return out
}

func canvasFor(id string) *Canvas { w, h := buildingSize(id); return New(w, h) }

// finish adds the ink outline every building gets.
func finish(c *Canvas) *Canvas { c.Outline(Ink); return c }

// column draws a marble column from top to bottom at x.
func column(c *Canvas, x, top, bottom int) {
	c.Rect(x, top, 3, bottom-top+1, Marble)
	c.VLine(x+2, top, bottom, MarbleShade)
	c.HLine(x-1, x+3, top, Marble)
	c.HLine(x-1, x+3, top+1, MarbleShade)
	c.HLine(x-1, x+3, bottom, MarbleShade)
}

// temple draws steps, columns, an entablature and a pediment. It returns
// the y of the top of the columns and the bottom of the columns.
func temple(c *Canvas, x, y, w, h, cols int, back uint8) (int, int) {
	pedH := h / 4
	c.Poly([]Pt{{x, y + pedH}, {x + w/2, y}, {x + w, y + pedH}}, Marble)
	c.Line(x, y+pedH, x+w/2, y, Terracotta)
	c.Line(x+w/2, y, x+w, y+pedH, Terracotta)
	c.Poly([]Pt{{x + 6, y + pedH - 1}, {x + w/2, y + 3}, {x + w - 6, y + pedH - 1}}, MarbleShade)
	ent := y + pedH
	c.Rect(x, ent, w+1, 4, Marble)
	c.HLine(x, x+w, ent+3, MarbleShade)
	for i := x + 2; i < x+w; i += 4 {
		c.Set(i, ent+1, MarbleShade)
	}
	top, bottom := ent+4, y+h-5
	c.Rect(x+2, top, w-3, bottom-top, back)
	step := (w - 6) / (cols - 1)
	for i := 0; i < cols; i++ {
		column(c, x+2+i*step, top, bottom)
	}
	c.Rect(x-1, bottom+1, w+3, 1, Marble)
	c.Rect(x-2, bottom+2, w+5, 1, MarbleShade)
	c.Rect(x-3, bottom+3, w+7, 1, Stone)
	return top, bottom
}

func gates(open bool) *Canvas {
	c := canvasFor("bldg/gates")
	for _, px := range []int{6, 46} {
		c.Rect(px, 14, 12, 38, Stone)
		c.VLine(px+11, 14, 51, StoneDark)
		for y := 18; y < 52; y += 6 {
			c.HLine(px, px+11, y, StoneDark)
		}
		c.Rect(px-1, 12, 14, 3, Marble)
	}
	c.Rect(4, 6, 56, 7, Marble)
	c.HLine(4, 59, 12, MarbleShade)
	c.Poly([]Pt{{8, 6}, {32, 1}, {56, 6}}, Marble)
	c.Line(8, 6, 32, 1, Terracotta)
	c.Line(32, 1, 56, 6, Terracotta)
	if open {
		c.Rect(18, 15, 28, 37, Sand)
		c.Dither(18, 40, 28, 12, Sand, Stone, 0.3)
		// banner
		c.Rect(28, 13, 8, 14, Terracotta)
		c.VLine(35, 13, 26, TerracottaDark)
		c.Set(31, 18, BronzeLight)
		c.Set(32, 18, BronzeLight)
		c.Set(29, 27, Terracotta)
		c.Set(34, 27, Terracotta)
	} else {
		c.Rect(18, 15, 28, 37, InkSoft)
		for x := 19; x < 46; x += 3 {
			c.VLine(x, 15, 51, Bronze)
		}
		c.HLine(18, 45, 22, BronzeLight)
		c.HLine(18, 45, 44, Bronze)
	}
	c.Rect(2, 52, 60, 3, Stone)
	return finish(c)
}

func library(lit bool) *Canvas {
	c := canvasFor("bldg/library")
	top, bottom := temple(c, 4, 2, 72, 52, 6, InkSoft)
	for i := 0; i < 5; i++ {
		x0 := 8 + i*13
		for y := top + 4; y < bottom-3; y += 7 {
			c.HLine(x0, x0+8, y+4, Bronze)
			for k := 0; k < 8; k += 2 {
				col := Sand
				if (k+y+i)%3 == 0 {
					col = Marble
				}
				c.Rect(x0+k, y+1, 2, 3, col)
			}
		}
		if lit {
			c.Set(x0+4, top+2, FireHot)
			c.Set(x0+4, top+3, Fire)
		}
	}
	return finish(c)
}

func council(lit bool, f int) *Canvas {
	c := canvasFor("bldg/council")
	c.Ellipse(32, 38, 24, 7, MarbleShade)
	c.Rect(8, 22, 49, 16, Marble)
	c.Ellipse(32, 38, 24, 5, Marble)
	for x := 10; x < 56; x += 6 {
		c.VLine(x+2, 23, 39, MarbleShade)
	}
	door := InkSoft
	if lit {
		door = FireHot
	}
	c.Rect(28, 28, 8, 12, door)
	if lit {
		c.Rect(29, 34, 6, 6, Fire)
	}
	c.Ellipse(32, 21, 27, 5, TerracottaDark)
	c.Ellipse(32, 19, 27, 5, Terracotta)
	c.Ellipse(32, 15, 20, 4, TerracottaDark)
	c.Ellipse(32, 13, 20, 4, Terracotta)
	c.Ellipse(32, 9, 12, 3, Terracotta)
	c.Rect(31, 3, 3, 4, Bronze)
	c.Set(32, 2, BronzeLight)
	c.Rect(2, 43, 60, 3, Stone)
	finish(c)
	if lit {
		// hearth smoke drifting up from the roof, drawn after the outline
		for i := 0; i < 3; i++ {
			c.Set(36+(i+f)%2, 1-i+f%2, Smoke)
		}
	}
	return c
}

func academy(lit bool) *Canvas {
	c := canvasFor("bldg/academy")
	OliveTree(c, 6, 44)
	OliveTree(c, 65, 44)
	OliveTree(c, 60, 50)
	c.Rect(12, 22, 48, 22, Marble)
	c.HLine(12, 59, 43, MarbleShade)
	c.Poly([]Pt{{8, 22}, {18, 10}, {54, 10}, {64, 22}}, Terracotta)
	c.HLine(8, 64, 22, TerracottaDark)
	for x := 12; x < 62; x += 4 {
		c.Line(x+4, 11, x, 21, TerracottaDark)
	}
	win := InkSoft
	if lit {
		win = FireHot
	}
	for _, x := range []int{17, 33, 49} {
		c.Rect(x, 27, 6, 7, win)
		c.HLine(x-1, x+6, 34, MarbleShade)
	}
	c.Rect(10, 46, 52, 3, Stone)
	// sundial
	c.Rect(5, 47, 3, 6, Stone)
	c.HLine(3, 9, 47, Marble)
	c.Set(6, 46, Ink)
	if lit {
		// easel with a plan on it
		c.Line(64, 53, 67, 38, Bronze)
		c.Line(71, 53, 67, 38, Bronze)
		c.Rect(61, 39, 10, 8, Marble)
		c.HLine(63, 69, 41, InkSoft)
		c.HLine(63, 67, 43, InkSoft)
		c.HLine(63, 68, 45, InkSoft)
	}
	return finish(c)
}

// workshop draws the forge; f < 0 is idle with embers only.
func workshop(f int) *Canvas {
	c := canvasFor("bldg/workshop")
	c.Rect(4, 16, 64, 34, Stone)
	for y := 20; y < 50; y += 5 {
		off := (y / 5 % 2) * 4
		c.HLine(4, 67, y, StoneDark)
		for x := 4 + off; x < 68; x += 8 {
			c.VLine(x, y-4, y, StoneDark)
		}
	}
	c.Rect(2, 12, 68, 5, TerracottaDark)
	c.HLine(2, 69, 12, Terracotta)
	// chimney
	c.Rect(54, 2, 8, 11, StoneDark)
	c.HLine(53, 62, 2, Stone)
	// open arch with the forge inside
	c.Rect(14, 24, 26, 26, Ink)
	c.Ellipse(27, 24, 13, 4, Ink)
	c.Rect(18, 40, 18, 10, StoneDark)
	if f < 0 {
		for _, x := range []int{22, 26, 30} {
			c.Set(x, 40, Fire)
		}
	} else {
		hs := [][]int{{6, 9, 5, 8, 7}, {8, 6, 9, 5, 7}, {5, 8, 7, 9, 6}, {9, 5, 6, 8, 9}}[f]
		for i, h := range hs {
			x := 20 + i*3
			c.Rect(x, 40-h, 3, h, Fire)
			c.Rect(x+1, 40-h+2, 1, h-2, FireHot)
		}
		c.Dither(14, 24, 26, 6, Ink, InkSoft, 0.3)
	}
	// anvil out front
	c.Rect(46, 42, 12, 3, StoneDark)
	c.Rect(49, 45, 6, 5, StoneDark)
	c.HLine(45, 58, 42, Stone)
	c.Rect(2, 50, 68, 3, Stone)
	finish(c)
	if f >= 0 {
		for i := 0; i < 3; i++ {
			c.Set(57+(i+f)%3, 1-i, Smoke)
		}
	}
	return c
}

func oracle(smoke uint8, f int, thick bool) *Canvas {
	c := canvasFor("bldg/oracle")
	// rock ledge
	c.Poly([]Pt{{2, 58}, {62, 58}, {58, 70}, {6, 70}}, StoneDark)
	c.Dither(4, 60, 56, 9, StoneDark, Stone, 0.25)
	temple(c, 6, 20, 52, 40, 4, InkSoft)
	// tripod with a bowl
	c.Line(28, 62, 32, 54, Bronze)
	c.Line(36, 62, 32, 54, Bronze)
	c.VLine(32, 54, 62, Bronze)
	c.Ellipse(32, 53, 5, 2, Bronze)
	c.HLine(28, 36, 52, BronzeLight)
	finish(c)
	// smoke column, drawn after the outline so it stays soft
	n := 4
	if thick {
		n = 12
	}
	for i := 0; i < n; i++ {
		y := 50 - i*3 - f%3
		x := 32 + []int{0, 1, 2, 1, 0, -1, -2, -1}[(i+f)%8]
		c.Set(x, y, smoke)
		c.Set(x+1, y-1, smoke)
		if thick && i > 2 {
			c.Set(x-1, y-1, smoke)
			c.Set(x, y-2, smoke)
		}
	}
	return c
}

func areopagus(court bool) *Canvas {
	c := canvasFor("bldg/areopagus")
	c.Ellipse(32, 36, 30, 11, Stone)
	c.Ellipse(32, 40, 30, 7, StoneDark)
	c.Dither(4, 36, 56, 8, Stone, StoneDark, 0.4)
	c.Ellipse(32, 28, 22, 7, Stone)
	for i, w := range []int{18, 14, 10} {
		y := 32 - i*4
		c.HLine(32-w, 32+w, y, Marble)
		c.HLine(32-w, 32+w, y+1, MarbleShade)
	}
	if court {
		c.VLine(32, 12, 22, Bronze)
		c.HLine(25, 39, 13, Bronze)
		c.Line(25, 13, 23, 18, InkSoft)
		c.Line(25, 13, 27, 18, InkSoft)
		c.Line(39, 13, 37, 18, InkSoft)
		c.Line(39, 13, 41, 18, InkSoft)
		c.HLine(22, 28, 18, BronzeLight)
		c.HLine(36, 42, 18, BronzeLight)
		c.Set(32, 11, BronzeLight)
	}
	return finish(c)
}

// symposium draws the banquet hall; f < 0 means torches unlit.
func symposium(f int) *Canvas {
	c := canvasFor("bldg/symposium")
	c.Rect(6, 20, 68, 28, Marble)
	c.Poly([]Pt{{2, 20}, {14, 8}, {66, 8}, {78, 20}}, Terracotta)
	c.HLine(2, 78, 20, TerracottaDark)
	c.Rect(12, 24, 56, 22, InkSoft)
	// couches and the krater
	for _, x := range []int{14, 50} {
		c.Rect(x, 38, 16, 4, ClothRed)
		c.HLine(x, x+15, 37, Marble)
		c.Rect(x, 42, 2, 3, Bronze)
		c.Rect(x+14, 42, 2, 3, Bronze)
	}
	c.Ellipse(40, 38, 5, 4, Bronze)
	c.Rect(37, 42, 7, 3, Bronze)
	c.HLine(35, 45, 34, BronzeLight)
	for _, x := range []int{8, 30, 48, 70} {
		column(c, x, 22, 46)
	}
	c.Rect(3, 47, 74, 3, Stone)
	finish(c)
	for _, x := range []int{4, 75} {
		c.Rect(x, 30, 2, 8, Bronze)
		if f >= 0 {
			c.Set(x, 28-f, Fire)
			c.Set(x+1, 29, FireHot)
			c.Set(x, 29, Fire)
		}
	}
	return c
}

func agora() *Canvas {
	c := canvasFor("bldg/agora")
	c.Poly([]Pt{{4, 24}, {92, 24}, {94, 46}, {2, 46}}, Sand)
	for y := 26; y < 46; y += 4 {
		c.HLine(4, 92, y, Stone)
	}
	for x := 8; x < 92; x += 8 {
		c.VLine(x, 24, 45, Stone)
	}
	// stoa along the back
	c.Rect(6, 8, 84, 16, InkSoft)
	c.Rect(4, 2, 88, 4, Terracotta)
	c.HLine(4, 91, 5, TerracottaDark)
	c.Rect(4, 6, 88, 3, Marble)
	for x := 6; x < 90; x += 8 {
		column(c, x, 9, 23)
	}
	// fountain
	c.Ellipse(48, 36, 9, 5, Marble)
	c.Ellipse(48, 36, 7, 3, Sea)
	c.Set(48, 34, Foam)
	c.Set(48, 33, Foam)
	c.Set(47, 35, Foam)
	c.Set(49, 35, Foam)
	return finish(c)
}

func harbour() *Canvas {
	c := canvasFor("bldg/harbour")
	// quay on the beach
	c.Rect(2, 18, 92, 10, Stone)
	c.HLine(2, 93, 18, Marble)
	for x := 2; x < 94; x += 8 {
		c.VLine(x, 19, 27, StoneDark)
	}
	// wooden pier into the sea
	c.Rect(20, 28, 14, 26, Bronze)
	for y := 30; y < 54; y += 3 {
		c.HLine(20, 33, y, InkSoft)
	}
	for _, y := range []int{34, 46} {
		c.Rect(18, y, 2, 6, InkSoft)
		c.Rect(34, y, 2, 6, InkSoft)
	}
	// amphorae and a small storehouse
	for i, x := range []int{60, 66, 72} {
		c.Ellipse(x, 14-(i%2), 2, 4, Terracotta)
		c.Set(x, 9-(i%2), TerracottaDark)
	}
	c.Rect(76, 4, 16, 14, Marble)
	c.Poly([]Pt{{74, 4}, {84, 0}, {94, 4}}, Terracotta)
	c.Rect(82, 10, 4, 8, InkSoft)
	return finish(c)
}

func trireme(sailing bool, f int) *Canvas {
	id := "bldg/trireme-docked"
	if sailing {
		id = "bldg/trireme-sailing"
	}
	c := canvasFor(id)
	base := c.H - 8
	c.Poly([]Pt{{2, base - 4}, {6, base}, {42, base}, {46, base - 6}, {40, base - 3}, {8, base - 3}}, InkSoft)
	c.HLine(8, 40, base-3, Bronze)
	c.Set(41, base-4, Marble)
	c.Set(42, base-4, Ink)
	c.VLine(24, 2, base-4, InkSoft)
	if sailing {
		c.Rect(14, 4, 21, base-10, Marble)
		c.Rect(14, 8, 21, 3, Terracotta)
		c.VLine(34, 4, base-7, MarbleShade)
		for i, x := 0, 10; x < 40; i, x = i+1, x+5 {
			dx := []int{-2, 2}[(f+i)%2]
			c.Line(x, base, x+dx, base+4, InkSoft)
		}
	} else {
		c.HLine(14, 34, 4, Marble)
		c.HLine(14, 34, 5, MarbleShade)
	}
	return finish(c)
}

// theatre is the small hillside theatre where singers perform; f < 0 is
// empty with torches out.
func theatre(f int) *Canvas {
	c := canvasFor("bldg/theatre")
	// seating tiers cut into the hill, back to front
	for i, r := range []int{36, 30, 24, 18} {
		c.Ellipse(40, 34, r, r/2+2, []uint8{StoneDark, Stone, StoneDark, Stone}[i])
	}
	for y := 0; y < c.H; y++ {
		for x := 0; x < c.W; x++ {
			if y > 34 {
				c.Erase(x, y)
			}
		}
	}
	for _, x := range []int{22, 40, 58} {
		c.Line(40, 34, x+(x-40)/2, 16, MarbleShade)
	}
	// round orchestra floor and the stage building behind it
	c.Ellipse(40, 38, 14, 5, Sand)
	c.Rect(22, 40, 36, 8, Marble)
	c.HLine(22, 57, 47, MarbleShade)
	for _, x := range []int{24, 32, 46, 54} {
		column(c, x, 41, 47)
	}
	c.Rect(18, 48, 44, 3, Stone)
	finish(c)
	for _, x := range []int{18, 61} {
		c.Rect(x, 38, 2, 9, Bronze)
		if f >= 0 {
			c.Set(x, 36-f, Fire)
			c.Set(x+1, 37, FireHot)
			c.Set(x, 37, Fire)
		}
	}
	return c
}
