// Package art draws every image the web app uses. All art is made in code,
// palette-indexed, so each asset is drawn once and the dusk version comes
// from swapping palette colours.
package art

import "image/color"

// Palette indices. 0 is transparent.
const (
	Clear uint8 = iota
	Ink
	InkSoft
	Marble
	MarbleShade
	Stone
	StoneDark
	Terracotta
	TerracottaDark
	Bronze
	BronzeLight
	Olive
	OliveDark
	Sand
	Sea
	SeaDeep
	Foam
	Sky
	SkyHigh
	Fire
	FireHot
	Smoke
	OK
	Warn
	Fail
	Skin
	SkinShade
	HairDark
	HairGrey
	ClothBlue
	ClothRed
	GrassLight
	Haze
)

type Swatch struct {
	Name      string
	Day, Dusk color.NRGBA
}

func hex(v uint32) color.NRGBA {
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
}

// Palette is the single source of colour. Order matches the constants above.
var Palette = []Swatch{
	{"clear", color.NRGBA{}, color.NRGBA{}},
	{"ink", hex(0x14213d), hex(0x0a1426)},
	{"ink-soft", hex(0x3d4f6b), hex(0x2a3550)},
	{"marble", hex(0xfbfaf6), hex(0xd4d9e8)},
	{"marble-shade", hex(0xcdd5df), hex(0x8e97b4)},
	{"stone", hex(0xb9b4a8), hex(0x6f7088)},
	{"stone-dark", hex(0x7d786e), hex(0x474a63)},
	{"terracotta", hex(0xb5523b), hex(0x8f4440)},
	{"terracotta-dark", hex(0x7e3424), hex(0x5e2a32)},
	{"bronze", hex(0xc49a2c), hex(0x9c7d3a)},
	{"bronze-light", hex(0xecd06a), hex(0xd9b862)},
	{"olive", hex(0x6b8a3a), hex(0x40593e)},
	{"olive-dark", hex(0x3f5a2c), hex(0x26392e)},
	{"sand", hex(0xeadcb5), hex(0xa69c86)},
	{"sea", hex(0x1f86c9), hex(0x24508a)},
	{"sea-deep", hex(0x0e4a8f), hex(0x142a5c)},
	{"foam", hex(0xd6f1fb), hex(0x9db8dc)},
	{"sky", hex(0x8fd0f5), hex(0xe7907a)},
	{"sky-high", hex(0x3d8fd8), hex(0x4a3f7a)},
	{"fire", hex(0xf08a24), hex(0xf59a30)},
	{"fire-hot", hex(0xffd35a), hex(0xffe08a)},
	{"smoke", hex(0xe4e8f2), hex(0xaaa6c6)},
	{"ok", hex(0x4f9a4a), hex(0x5fb05a)},
	{"warn", hex(0xe0a020), hex(0xf0b030)},
	{"fail", hex(0xc03a32), hex(0xe0503a)},
	{"skin", hex(0xe0a878), hex(0xb88068)},
	{"skin-shade", hex(0xb87850), hex(0x8a5a4a)},
	{"hair-dark", hex(0x3a2a20), hex(0x2a1e1e)},
	{"hair-grey", hex(0xb8b0a8), hex(0x8a8290)},
	{"cloth-blue", hex(0x1d4e89), hex(0x26406e)},
	{"cloth-red", hex(0xa63d33), hex(0x7a2e38)},
	{"grass-light", hex(0x93a84a), hex(0x5e6e44)},
	{"haze", hex(0xd8eefa), hex(0xf4b48a)},
}

// Lookup returns the palette index for a swatch name.
func Lookup(name string) (uint8, bool) {
	for i, s := range Palette {
		if s.Name == name {
			return uint8(i), true
		}
	}
	return 0, false
}
