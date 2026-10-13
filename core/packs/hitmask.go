package packs

import (
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"sort"
)

// mask is a w x h boolean grid over one cell (Rust HitMask).
type mask struct {
	w, h int
	bits []bool
}

// newMask is an all-clear mask, or an empty one when w*h is unreasonably large.
func newMask(w, h uint64) *mask {
	if w*h > math.MaxUint32 || (w != 0 && w*h/w != h) {
		return &mask{}
	}
	return &mask{w: int(w), h: int(h), bits: make([]bool, w*h)}
}

func (m *mask) contains(x, y int) bool {
	return x >= 0 && y >= 0 && x < m.w && y < m.h && m.bits[y*m.w+x]
}

// set ignores positions outside the mask, including negative ones.
func (m *mask) set(x, y int64) {
	if x >= 0 && y >= 0 && x < int64(m.w) && y < int64(m.h) {
		m.bits[int(y)*m.w+int(x)] = true
	}
}

// pack encodes the mask row-major, LSB-first: pixel (x, y) is bit (y*w+x)%8 of byte (y*w+x)/8.
func (m *mask) pack() []byte {
	out := make([]byte, (len(m.bits)+7)/8)
	for i, b := range m.bits {
		if b {
			out[i/8] |= 1 << (i % 8)
		}
	}
	return out
}

// stamp copies the non-transparent pixels of a size region at src in the sheet to at in the mask.
func stamp(m *mask, sheet image.Image, src [2]uint64, size [2]uint32, at [2]int64) {
	b := sheet.Bounds()
	for y := 0; y < int(min(uint64(size[1]), uint64(m.h))); y++ {
		for x := 0; x < int(min(uint64(size[0]), uint64(m.w))); x++ {
			sx, sy := src[0]+uint64(x), src[1]+uint64(y)
			if sx >= uint64(b.Dx()) || sy >= uint64(b.Dy()) {
				continue
			}
			if _, _, _, a := sheet.At(b.Min.X+int(sx), b.Min.Y+int(sy)).RGBA(); a > 0 {
				m.set(at[0]+int64(x), at[1]+int64(y))
			}
		}
	}
}

// buildMask is the union of every body frame, the mouth at every level and every faceDy,
// and the body shifted down by up to bob.px (hit_mask.rs).
func buildMask(sheet image.Image, p Pack) *mask {
	cw, ch := uint64(p.Cell[0]), uint64(p.Cell[1])
	m := newMask(cw, ch)
	for _, s := range p.States {
		for f := uint64(0); f < uint64(s.Frames); f++ {
			stamp(m, sheet, [2]uint64{f * cw, uint64(s.Row) * ch}, p.Cell, [2]int64{})
		}
	}
	mo := p.Mouth
	dys := map[int32]bool{0: true}
	for _, s := range p.States {
		for _, d := range s.FaceDy {
			dys[d] = true
		}
	}
	sorted := make([]int32, 0, len(dys))
	for d := range dys {
		sorted = append(sorted, d)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for level := uint64(0); level < uint64(mo.Levels); level++ {
		for _, dy := range sorted {
			src := [2]uint64{level * cw, uint64(mo.Row) * ch}
			stamp(m, sheet, src, mo.Size, [2]int64{int64(mo.Anchor[0]), int64(mo.Anchor[1]) + int64(dy)})
		}
	}
	base := append([]bool(nil), m.bits...)
	bob := int64(min(uint64(p.Bob.Px), uint64(m.h)))
	for y := 0; y < m.h; y++ {
		for x := 0; x < m.w; x++ {
			if base[y*m.w+x] {
				for d := int64(1); d <= bob; d++ {
					m.set(int64(x), int64(y)+d)
				}
			}
		}
	}
	return m
}

// HitMask decodes the pack's sheet and returns the cell-sized hit mask as a row-major,
// LSB-first bitset (pixel (x, y) is bit (y*w+x)%8 of bits[(y*w+x)/8]).
func (p Pack) HitMask() (w, h int, bits []byte, err error) {
	f, err := os.Open(p.SheetPath)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("hit mask: %w", err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("hit mask: decode %s: %w", p.SheetPath, err)
	}
	m := buildMask(img, p)
	return m.w, m.h, m.pack(), nil
}

// HitMaskBase64 is the pack event's hitMask field: the HitMask bitset, standard base64.
func HitMaskBase64(p Pack) (string, error) {
	_, _, bits, err := p.HitMask()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(bits), nil
}
