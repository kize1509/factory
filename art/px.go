package art

import (
	"fmt"
	"strings"
)

// ParsePX reads a text-grid asset. Lines starting with # are comments,
// lines like "k=ink" map a character to a palette swatch, "." is
// transparent, and a blank line separates animation frames.
func ParsePX(src string) ([]*Canvas, error) {
	legend := map[rune]uint8{'.': Clear}
	var frames []*Canvas
	var rows []string
	flush := func() error {
		if len(rows) == 0 {
			return nil
		}
		f := New(len([]rune(rows[0])), len(rows))
		for y, r := range rows {
			rs := []rune(r)
			if len(rs) != f.W {
				return fmt.Errorf("row %d is %d wide, want %d", y, len(rs), f.W)
			}
			for x, ch := range rs {
				p, ok := legend[ch]
				if !ok {
					return fmt.Errorf("row %d: %q is not in the legend", y, ch)
				}
				f.Set(x, y, p)
			}
		}
		frames = append(frames, f)
		rows = nil
		return nil
	}
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimRight(line, " \r")
		switch {
		case strings.HasPrefix(line, "#"):
		case strings.Contains(line, "="):
			k, v, _ := strings.Cut(line, "=")
			p, ok := Lookup(strings.TrimSpace(v))
			if !ok {
				return nil, fmt.Errorf("unknown swatch %q", v)
			}
			legend[[]rune(strings.TrimSpace(k))[0]] = p
		case line == "":
			if err := flush(); err != nil {
				return nil, err
			}
		default:
			rows = append(rows, line)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("no frames")
	}
	for i, f := range frames {
		if f.W != frames[0].W || f.H != frames[0].H {
			return nil, fmt.Errorf("frame %d is %dx%d, want %dx%d", i, f.W, f.H, frames[0].W, frames[0].H)
		}
	}
	return frames, nil
}
