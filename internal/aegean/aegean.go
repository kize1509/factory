// Package aegean is the lattice behind the Aegean map. The sea is a grid of
// hexagonal cells; every job owns one cell, which holds its island. The cell
// is chosen once, when the job starts, and stored with the job, so an island
// never moves when the page reloads, the engine restarts or a job in another
// country goes away.
//
// A project's jobs form a country: a cluster of cells around the project's
// capital. A cell that touches another country's cell is off limits, so a
// one-cell channel of open sea always lies between two countries, however
// they grow, and a country's outline never meets a neighbour's.
package aegean

import "math"

// The lattice. Rows are PitchY apart and every other row sits half a pitch to
// the right; a cell's six neighbours are at about the same distance. The sea
// is 3000×1800; the mainland is along the top and the west edge.
const (
	PitchX, PitchY   = 200, 150
	OriginX, OriginY = 450, 380
	Cols, Rows       = 12, 9
	Cells            = Cols * Rows
)

// None means a job has no cell yet (or the lattice is full).
const None = -1

// Pos is the centre of a cell, in map pixels.
func Pos(c int) (x, y float64) {
	row, col := c/Cols, c%Cols
	x = OriginX + float64(col*PitchX)
	if row%2 == 1 {
		x += PitchX / 2
	}
	return x, OriginY + float64(row*PitchY)
}

// Neighbours are the cells that touch c.
func Neighbours(c int) []int {
	row, col := c/Cols, c%Cols
	d := -1 // the row above and below touch columns col+d and col+d+1
	if row%2 == 1 {
		d = 0
	}
	var out []int
	for _, n := range [][2]int{{row, col - 1}, {row, col + 1}, {row - 1, col + d}, {row - 1, col + d + 1}, {row + 1, col + d}, {row + 1, col + d + 1}} {
		if n[0] >= 0 && n[0] < Rows && n[1] >= 0 && n[1] < Cols {
			out = append(out, n[0]*Cols+n[1])
		}
	}
	return out
}

// Dist is the distance between two cell centres.
func Dist(a, b int) float64 {
	ax, ay := Pos(a)
	bx, by := Pos(b)
	return math.Hypot(ax-bx, ay-by)
}

// Placed is one job's island: the project it belongs to and its cell.
type Placed struct {
	Project string
	Cell    int
}

// state is what is taken, by whom.
type state struct {
	owner map[int]string
	all   []Placed
}

func newState(placed []Placed) *state {
	s := &state{owner: map[int]string{}, all: placed}
	for _, p := range placed {
		if p.Cell >= 0 && p.Cell < Cells {
			s.owner[p.Cell] = p.Project
		}
	}
	return s
}

// foreign tells whether c touches, or is, a cell of another project.
func (s *state) foreign(c int, project string) bool {
	if o, ok := s.owner[c]; ok && o != project {
		return true
	}
	for _, n := range Neighbours(c) {
		if o, ok := s.owner[n]; ok && o != project {
			return true
		}
	}
	return false
}

// capital is the cell of the project's oldest job still there.
func (s *state) capital(project string) (int, bool) {
	for _, p := range s.all {
		if p.Project == project && p.Cell >= 0 {
			return p.Cell, true
		}
	}
	return None, false
}

const (
	roomCap  = 900 // distance from other capitals that counts as plenty of room
	shoreCap = 450 // likewise from the mainland
)

// shore is how far a cell is from the mainland's coast, which runs along the
// top and the west of the map.
func shore(c int) float64 {
	x, y := Pos(c)
	return math.Min(y-240, x-180)
}

// Claim picks the cell for a project's new job. placed holds every job that
// has a cell, oldest first; Claim never changes any of them.
//
// The project's first job takes a capital: the free cell with the most room,
// meaning the farthest from every other capital and from the mainland. Each
// later job takes the free cell next to its own country that is nearest its
// capital, so a country grows as a tidy cluster. Cells that touch another
// country are never taken. It returns None when the sea is full.
func Claim(placed []Placed, project string) int {
	s := newState(placed)
	free := func(c int) bool { _, taken := s.owner[c]; return !taken }
	if cap, ok := s.capital(project); ok {
		own := func(c int) bool { return s.owner[c] == project }
		best, bs := None, math.Inf(1)
		pick := func(adjacentOnly, strict bool) {
			for c := 0; c < Cells; c++ {
				if !free(c) || (strict && s.foreign(c, project)) {
					continue
				}
				mine := 0
				for _, n := range Neighbours(c) {
					if own(n) {
						mine++
					}
				}
				if adjacentOnly && mine == 0 {
					continue
				}
				// nearest the capital; among equals, the one hugging more of the country
				if sc := Dist(c, cap) - float64(mine)*4; sc < bs-1e-9 {
					best, bs = c, sc
				}
			}
		}
		for _, mode := range [][2]bool{{true, true}, {false, true}, {true, false}, {false, false}} {
			if pick(mode[0], mode[1]); best != None {
				return best
			}
		}
		return None
	}
	// a new country
	var caps []int
	seen := map[string]bool{}
	for _, p := range placed {
		if p.Cell >= 0 && !seen[p.Project] {
			seen[p.Project] = true
			c, _ := s.capital(p.Project)
			caps = append(caps, c)
		}
	}
	cx, cy := float64(OriginX+(Cols-1)*PitchX/2), float64(OriginY+(Rows-1)*PitchY/2)
	best, bs, bd := None, math.Inf(-1), math.Inf(1)
	for _, strict := range []bool{true, false} {
		for c := 0; c < Cells; c++ {
			if !free(c) || (strict && s.foreign(c, project)) {
				continue
			}
			room := 2 * math.Min(shore(c), shoreCap)
			for _, k := range caps {
				room = math.Min(room, Dist(c, k))
			}
			room = math.Min(room, roomCap)
			x, y := Pos(c)
			if d := math.Hypot(x-cx, y-cy); room > bs+1e-9 || (math.Abs(room-bs) <= 1e-9 && d < bd) {
				best, bs, bd = c, room, d
			}
		}
		if best != None {
			return best
		}
	}
	return None
}

// CloseUp closes a country up after one of its jobs went away. placed holds
// the jobs that are left, oldest first; freed is the cell that was vacated. If
// the country's outermost island, the one farthest from its capital, is
// farther out than the freed cell, it moves in. It returns the index in
// placed of the island that moves and its new cell, or -1.
func CloseUp(placed []Placed, project string, freed int) (int, int) {
	s := newState(placed)
	cap, ok := s.capital(project)
	if !ok || freed < 0 {
		return -1, None
	}
	if _, taken := s.owner[freed]; taken {
		return -1, None
	}
	out, od := -1, 0.0
	for i, p := range placed {
		if p.Project == project && p.Cell >= 0 {
			if d := Dist(p.Cell, cap); d >= od {
				out, od = i, d // among equals the newest goes
			}
		}
	}
	if out < 0 || Dist(freed, cap) >= od-1e-9 {
		return -1, None
	}
	return out, freed
}
