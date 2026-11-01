package art

// Landmarks are things around the city to discover. They don't host work.

func init() {
	sizes["bldg/olympus"] = [2]int{200, 150}
	sizes["bldg/horse"] = [2]int{48, 44}
	sizes["bldg/poseidon"] = [2]int{80, 90}
	sizes["bldg/pharos"] = [2]int{48, 80}
	sizes["bldg/colossus"] = [2]int{56, 84}
}

// Landmarks returns every landmark with its states.
func Landmarks() []Building {
	return []Building{
		{"bldg/olympus", 200, 150, map[string][]*Canvas{"idle": {olympus(false)}, "active": {olympus(true)}}},
		{"bldg/horse", 48, 44, map[string][]*Canvas{"idle": {horse()}}},
		{"bldg/poseidon", 80, 90, map[string][]*Canvas{"idle": {poseidon()}}},
		{"bldg/pharos", 48, 80, map[string][]*Canvas{"idle": frames(4, pharos)}},
		{"bldg/colossus", 56, 84, map[string][]*Canvas{"idle": frames(2, colossus)}},
	}
}

// olympus is the home of the gods: a tall snowy peak in the clouds with a
// small golden temple on top. Active adds a lightning bolt.
func olympus(bolt bool) *Canvas {
	c := canvasFor("bldg/olympus")
	c.Poly([]Pt{{0, 150}, {60, 70}, {84, 54}, {100, 26}, {116, 50}, {140, 64}, {200, 150}}, Stone)
	c.Poly([]Pt{{100, 26}, {116, 50}, {140, 64}, {200, 150}, {130, 150}, {124, 90}, {108, 50}}, StoneDark)
	c.Poly([]Pt{{80, 58}, {100, 26}, {120, 54}, {110, 60}, {100, 52}, {90, 64}}, Marble)
	c.Poly([]Pt{{100, 26}, {120, 54}, {110, 60}, {108, 46}}, MarbleShade)
	// golden temple on the summit
	c.Rect(93, 18, 14, 6, BronzeLight)
	c.Poly([]Pt{{91, 18}, {100, 12}, {109, 18}}, Bronze)
	for x := 94; x < 107; x += 3 {
		c.VLine(x, 18, 23, Bronze)
	}
	// clouds around the middle of the mountain
	for _, p := range [][3]int{{40, 96, 14}, {66, 90, 12}, {150, 100, 16}, {176, 94, 10}, {120, 104, 12}} {
		c.Ellipse(p[0], p[1]+2, p[2], p[2]/2+1, MarbleShade)
		c.Ellipse(p[0], p[1], p[2], p[2]/2, Marble)
	}
	finish(c)
	if bolt {
		pts := []Pt{{126, 4}, {120, 14}, {126, 16}, {118, 30}, {130, 18}, {124, 16}, {132, 4}}
		for i := 0; i+1 < len(pts); i++ {
			c.Line(pts[i].X, pts[i].Y, pts[i+1].X, pts[i+1].Y, FireHot)
		}
	}
	return c
}

// horse is the wooden horse standing on the plain.
func horse() *Canvas {
	c := canvasFor("bldg/horse")
	c.Rect(10, 18, 26, 12, Bronze)
	c.HLine(10, 35, 18, BronzeLight)
	for x := 12; x < 36; x += 5 {
		c.VLine(x, 19, 29, InkSoft)
	}
	c.Poly([]Pt{{30, 20}, {36, 6}, {42, 6}, {40, 14}, {36, 22}}, Bronze)
	c.Rect(38, 4, 8, 5, Bronze)
	c.Set(42, 5, Ink)
	c.VLine(36, 2, 5, InkSoft)
	for _, x := range []int{12, 18, 28, 33} {
		c.Rect(x, 30, 3, 7, Bronze)
	}
	c.Rect(8, 37, 32, 2, InkSoft)
	for _, x := range []int{11, 34} {
		c.Circle(x, 40, 3, StoneDark)
		c.Set(x, 40, Stone)
	}
	c.Line(10, 20, 5, 28, Bronze) // tail
	return finish(c)
}

// poseidon is the temple on the cliff of the east cape, with a trident.
func poseidon() *Canvas {
	c := canvasFor("bldg/poseidon")
	c.Poly([]Pt{{0, 90}, {4, 58}, {76, 58}, {80, 90}}, Stone)
	c.Dither(2, 64, 76, 24, Stone, StoneDark, 0.3)
	temple(c, 12, 20, 56, 40, 5, Sea)
	// trident statue beside it
	c.VLine(74, 6, 56, Bronze)
	c.HLine(70, 78, 10, Bronze)
	c.VLine(70, 4, 10, Bronze)
	c.VLine(78, 4, 10, Bronze)
	c.Set(74, 3, BronzeLight)
	return finish(c)
}

// pharos is the lighthouse on its island; the light turns over 4 frames.
func pharos(f int) *Canvas {
	c := canvasFor("bldg/pharos")
	c.Ellipse(24, 74, 22, 5, Stone)
	c.Ellipse(24, 72, 18, 3, Sand)
	c.Rect(14, 50, 20, 22, Marble)
	c.VLine(33, 50, 71, MarbleShade)
	c.Rect(17, 30, 14, 20, Marble)
	c.VLine(30, 30, 49, MarbleShade)
	c.Rect(19, 16, 10, 14, Marble)
	c.VLine(28, 16, 29, MarbleShade)
	for _, y := range []int{30, 50} {
		c.HLine(12, 35, y, Bronze)
	}
	c.Rect(20, 10, 8, 6, Bronze)
	c.Rect(22, 11, 4, 4, FireHot)
	c.Rect(22, 58, 4, 8, InkSoft)
	finish(c)
	// beam sweeping left and right
	dir := []int{-1, 0, 1, 0}[f]
	if dir != 0 {
		for i := 1; i < 20; i++ {
			x := 24 + dir*(4+i)
			c.Set(x, 13-i/6, FireHot)
			if i%2 == 0 {
				c.Set(x, 12-i/5, Smoke)
			}
		}
	} else {
		c.Set(24, 8, FireHot)
		c.Set(24, 7, Smoke)
	}
	return c
}

// colossus is the bronze giant standing over the harbour mouth of its island,
// holding a torch that flickers.
func colossus(f int) *Canvas {
	c := canvasFor("bldg/colossus")
	c.Ellipse(28, 79, 26, 5, Stone)
	c.Rect(4, 66, 14, 12, Marble)
	c.Rect(38, 66, 14, 12, Marble)
	c.HLine(4, 17, 66, MarbleShade)
	c.HLine(38, 51, 66, MarbleShade)
	// legs apart, one on each pedestal
	c.Line(11, 65, 22, 40, Bronze)
	c.Line(12, 65, 23, 40, Bronze)
	c.Line(44, 65, 33, 40, Bronze)
	c.Line(45, 65, 34, 40, Bronze)
	c.Rect(21, 22, 14, 20, Bronze)
	c.VLine(34, 22, 41, InkSoft)
	c.Rect(21, 36, 14, 4, BronzeLight) // belt
	c.Ellipse(28, 16, 5, 6, Bronze)
	c.Set(26, 15, InkSoft)
	c.Set(30, 15, InkSoft)
	for _, x := range []int{23, 26, 29, 32} { // crown of rays
		c.VLine(x, 7, 10, BronzeLight)
	}
	// raised arm with torch
	c.Line(35, 24, 44, 8, Bronze)
	c.Line(35, 25, 45, 9, Bronze)
	c.Rect(43, 5, 3, 4, Bronze)
	c.Line(21, 25, 16, 38, Bronze)
	finish(c)
	c.Set(44, 3-f, Fire)
	c.Set(44, 4, FireHot)
	c.Set(45, 3, Fire)
	return c
}
