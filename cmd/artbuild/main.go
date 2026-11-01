// Command artbuild draws every asset in the catalogue and writes the PNGs,
// a manifest and the preview page to web/assets.
//
//	go run ./cmd/artbuild
package main

import (
	"encoding/json"
	"fmt"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"factory/art"
)

const out = "web/assets"

type state struct {
	File   string `json:"file"`
	Frames int    `json:"frames"`
}

type asset struct {
	W      int              `json:"w"`
	H      int              `json:"h"`
	States map[string]state `json:"states"`
}

type manifest struct {
	Scene        [2]int            `json:"scene"`
	City         [2]int            `json:"city"`
	Map          [2]int            `json:"map"`
	MapSlots     []art.Pt          `json:"mapSlots"`
	MapCountries []art.Country     `json:"mapCountries"`
	Layout       []art.Place       `json:"layout"`
	Assets       map[string]asset  `json:"assets"`
	Philosophers []art.Philosopher `json:"philosophers"`
	Palette      []string          `json:"palette"`
	PlaceAbout   map[string]string `json:"placeAbout"`
	PeopleAbout  map[string]string `json:"peopleAbout"`
}

var m = manifest{Scene: [2]int{art.SceneW * Zoom, art.SceneH * Zoom}, City: [2]int{art.OX * Zoom, art.OY * Zoom}, Assets: map[string]asset{}}

// Zoom is how much bigger the world is drawn than the people in it. The
// scenery, buildings and landmarks are enlarged by it; portraits are not, so
// agents fit in front of and inside the buildings.
const Zoom = 2

// big enlarges every frame by Zoom.
func big(frames ...*art.Canvas) []*art.Canvas {
	out := make([]*art.Canvas, len(frames))
	for i, f := range frames {
		out[i] = art.Scale(f, Zoom)
	}
	return out
}

func main() {
	if err := os.RemoveAll(out); err != nil {
		log.Fatal(err)
	}
	add("scene/base", "idle", big(art.SceneBase())...)
	add("scene/water", "idle", big(art.Water()...)...)
	add("scene/stars", "idle", big(art.Stars()...)...)
	for _, b := range append(art.Buildings(), art.Landmarks()...) {
		for name, frames := range b.States {
			add(b.ID, name, big(frames...)...)
		}
	}
	for _, p := range art.Philosophers {
		add("portrait/"+p.ID, "idle", art.Portrait32(p))
		add("portrait/"+p.ID, "talk", art.PortraitTalk(p)...)
		add("portrait/"+p.ID, "wave", art.PortraitWave(p)...)
		add("token/"+p.ID, "idle", art.Token16(p))
	}
	add("frame/bronze-32", "idle", art.Frame(15))
	add("frame/bronze-16", "idle", art.Frame(7))
	add("ring/thinking", "idle", art.Ring(art.SkyHigh, true, 8, 19)...)
	add("ring/tool", "idle", art.Ring(art.BronzeLight, true, 8, 19)...)
	add("ring/waiting", "idle", art.Ring(art.Warn, false, 2, 19)...)
	add("ring/blocked", "idle", art.Ring(art.Warn, false, 1, 19)...)
	add("ring/done", "idle", art.Ring(art.OK, false, 1, 19)...)
	add("ring/failed", "idle", art.Ring(art.Fail, false, 2, 19)...)
	add("ring/interrupted", "idle", art.Ring(art.Foam, true, 8, 19)...)
	for v := 0; v < 3; v++ {
		add(fmt.Sprintf("scene/cloud-%d", v), "idle", big(art.Cloud(v))...)
	}
	add("scene/birds", "idle", big(art.Birds()...)...)
	add("fx/ufo", "idle", big(art.UFO()...)...)
	add("fx/beam", "idle", big(art.Beam()...)...)
	add("fx/notes", "idle", art.Notes()...)
	add("fx/scroll", "idle", big(art.Scroll()...)...)
	add("fx/decks", "idle", big(art.Decks()...)...)
	add("map/base", "idle", art.AegeanMap())
	add("map/polis", "empty", art.Polis("empty", 0))
	add("map/polis", "active", art.Polis("active", 0), art.Polis("active", 1))
	add("map/polis", "idle", art.Polis("idle", 0))
	add("map/polis", "done", art.Polis("done", 0))
	add("map/boat", "idle", art.MapBoat()...)
	icons()

	// place positions in the enlarged world; tokens gather in front
	for _, p := range art.Layout {
		a := m.Assets[p.Asset]
		p.X, p.Y = p.X*Zoom, p.Y*Zoom
		p.Front = art.Pt{X: p.X + a.W/2, Y: p.Y + a.H - 26}
		m.Layout = append(m.Layout, p)
	}
	m.Map, m.MapSlots, m.MapCountries = [2]int{art.MapW, art.MapH}, art.MapSlots, art.Countries
	m.Philosophers = art.Philosophers
	m.PlaceAbout, m.PeopleAbout = art.PlaceAbout, art.PeopleAbout
	for _, p := range art.Layout {
		if art.PlaceAbout[p.ID] == "" {
			log.Fatalf("place %s has no legend text", p.ID)
		}
	}
	for _, p := range art.Philosophers {
		if art.PeopleAbout[p.ID] == "" {
			log.Fatalf("%s has no legend text", p.ID)
		}
	}
	for _, s := range art.Palette {
		m.Palette = append(m.Palette, s.Name)
	}
	js, err := json.MarshalIndent(m, "", " ")
	check(err)
	check(os.WriteFile(filepath.Join(out, "manifest.js"), append([]byte("window.ASSETS = "), js...), 0o644))
	page, err := os.ReadFile("art/preview/preview.html")
	check(err)
	// stamp the manifest URL so browsers never use a stale copy
	page = []byte(strings.Replace(string(page), `src="manifest.js"`, fmt.Sprintf(`src="manifest.js?v=%d"`, time.Now().Unix()), 1))
	check(os.WriteFile(filepath.Join(out, "preview.html"), page, 0o644))
	fmt.Printf("wrote %d assets to %s\n", len(m.Assets), out)
}

// icons reads every text grid under art/src/icon.
func icons() {
	files, err := filepath.Glob("art/src/icon/*.px")
	check(err)
	sort.Strings(files)
	for _, f := range files {
		src, err := os.ReadFile(f)
		check(err)
		frames, err := art.ParsePX(string(src))
		if err != nil {
			log.Fatalf("%s: %v", f, err)
		}
		add("icon/"+strings.TrimSuffix(filepath.Base(f), ".px"), "idle", frames...)
	}
}

// add writes one state of an asset as a day and a dusk strip.
func add(id, name string, frames ...*art.Canvas) {
	a, ok := m.Assets[id]
	if !ok {
		a = asset{W: frames[0].W, H: frames[0].H, States: map[string]state{}}
	}
	if frames[0].W != a.W || frames[0].H != a.H {
		log.Fatalf("%s/%s is %dx%d, other states are %dx%d", id, name, frames[0].W, frames[0].H, a.W, a.H)
	}
	file := id + "." + name
	strip := art.Strip(frames...)
	write(file+".png", strip, false)
	write(file+".dusk.png", strip, true)
	a.States[name] = state{File: file, Frames: len(frames)}
	m.Assets[id] = a
}

func write(name string, c *art.Canvas, dusk bool) {
	path := filepath.Join(out, name)
	check(os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path)
	check(err)
	defer f.Close()
	check(png.Encode(f, c.Image(dusk)))
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
