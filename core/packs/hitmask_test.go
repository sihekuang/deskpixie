package packs

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Ports of mascot-pack hit_mask.rs tests (9) plus Go-only tests for the bitset/base64
// encoding and file decoding (HitMask, HitMaskBase64).

func tinyPack() Pack {
	return Pack{
		Cell: [2]uint32{4, 4}, FPS: 8, Bob: Bob{Px: 1, Frames: 1},
		States: map[string]State{"idle": {Row: 0, Frames: 2, Loop: true, Bob: true, FaceDy: []int32{0, 0}}},
		Mouth:  Mouth{Row: 1, Anchor: [2]uint32{2, 0}, Size: [2]uint32{1, 1}, Levels: 2},
	}
}

func put(img *image.NRGBA, x, y int) { img.SetNRGBA(x, y, color.NRGBA{1, 2, 3, 255}) }

func setPixels(m *mask) [][2]int {
	var out [][2]int
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if m.contains(x, y) {
				out = append(out, [2]int{x, y})
			}
		}
	}
	return out
}

func TestHitUnionOfFramesRestingMouthAndBob(t *testing.T) {
	sheet := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	put(sheet, 1, 1) // frame 0 -> cell (1,1)
	put(sheet, 7, 2) // frame 1 -> cell (3,2)
	put(sheet, 0, 4) // mouth level 0 -> stamped at anchor (2,0)
	got := setPixels(buildMask(sheet, tinyPack()))
	want := [][2]int{{2, 0}, {1, 1}, {2, 1}, {1, 2}, {3, 2}, {3, 3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestHitOpenMouthLevelsAreGrabbable(t *testing.T) {
	sheet := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	put(sheet, 4, 4) // mouth level 1
	got := setPixels(buildMask(sheet, tinyPack()))
	if want := [][2]int{{2, 0}, {2, 1}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestHitMouthFollowsFaceDy(t *testing.T) {
	p := tinyPack()
	p.Bob.Px = 0
	p.States["idle"] = State{Row: 0, Frames: 2, FaceDy: []int32{0, 2}}
	sheet := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	put(sheet, 0, 4)
	got := setPixels(buildMask(sheet, p))
	if want := [][2]int{{2, 0}, {2, 2}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestHitNegativeFaceDyAboveCellIsDropped(t *testing.T) {
	p := tinyPack()
	p.Bob.Px = 0
	p.States["idle"] = State{Row: 0, Frames: 2, FaceDy: []int32{0, -1}}
	sheet := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	put(sheet, 0, 4)
	got := setPixels(buildMask(sheet, p))
	if want := [][2]int{{2, 0}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestHitHugeBobIsBoundedByCell(t *testing.T) {
	p := tinyPack()
	p.Bob.Px = 1<<32 - 1
	sheet := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	put(sheet, 1, 1)
	got := setPixels(buildMask(sheet, p))
	if want := [][2]int{{1, 1}, {1, 2}, {1, 3}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestHitOverflowingCellGivesEmptyMask(t *testing.T) {
	p := tinyPack()
	p.Cell = [2]uint32{1<<32 - 1, 1 << 31}
	m := buildMask(image.NewNRGBA(image.Rect(0, 0, 1, 1)), p)
	if m.contains(0, 0) || m.w != 0 || m.h != 0 {
		t.Fatalf("mask not empty: %dx%d", m.w, m.h)
	}
}

func TestHitOutOfBoundsIsNotContained(t *testing.T) {
	m := buildMask(image.NewNRGBA(image.Rect(0, 0, 8, 8)), tinyPack())
	if m.contains(4, 0) || m.contains(0, 4) || m.contains(-1, 0) || m.contains(1<<40, 1<<40) {
		t.Fatal("out-of-bounds contained")
	}
}

func TestHitBitMaskCoversBodyNotCorners(t *testing.T) {
	bit := mustLoad(t)["bit"]
	w, h, bits, err := bit.HitMask()
	if err != nil {
		t.Fatal(err)
	}
	if w != 32 || h != 32 || len(bits) != 32*32/8 {
		t.Fatalf("size %dx%d len %d", w, h, len(bits))
	}
	at := func(x, y int) bool { i := y*w + x; return bits[i/8]&(1<<(i%8)) != 0 }
	if !at(16, 18) {
		t.Error("centre of the body not set")
	}
	if at(0, 0) || at(31, 0) {
		t.Error("corner set")
	}
}

func TestHitFromPNGMatchesFromImage(t *testing.T) {
	bit := mustLoad(t)["bit"]
	f, err := os.Open(bit.SheetPath)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	m := buildMask(img, bit)
	w, h, bits, err := bit.HitMask()
	if err != nil {
		t.Fatal(err)
	}
	if w != int(m.w) || h != int(m.h) || !bytes.Equal(bits, m.pack()) {
		t.Fatal("HitMask differs from buildMask on the decoded sheet")
	}
	// Not a PNG -> error, not a panic.
	dir := t.TempDir()
	bad := bit
	bad.SheetPath = filepath.Join(dir, "x.png")
	if err := os.WriteFile(bad.SheetPath, []byte("not a png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := bad.HitMask(); err == nil {
		t.Fatal("want error for a non-PNG sheet")
	}
	missing := bit
	missing.SheetPath = filepath.Join(dir, "nope.png")
	if _, _, _, err := missing.HitMask(); err == nil {
		t.Fatal("want error for a missing sheet")
	}
}

func TestHitMaskBase64(t *testing.T) {
	bit := mustLoad(t)["bit"]
	_, _, bits, err := bit.HitMask()
	if err != nil {
		t.Fatal(err)
	}
	s, err := HitMaskBase64(bit)
	if err != nil {
		t.Fatal(err)
	}
	if s != base64.StdEncoding.EncodeToString(bits) {
		t.Fatal("base64 does not match the bitset")
	}
	if _, err := HitMaskBase64(Pack{}); err == nil {
		t.Fatal("want error for a pack without a sheet")
	}
}

func TestMaskPackBitOrder(t *testing.T) {
	m := newMask(3, 3)
	m.set(0, 0)
	m.set(2, 1) // index 5
	m.set(2, 2) // index 8 -> second byte bit 0
	if got := m.pack(); !bytes.Equal(got, []byte{0b00100001, 0b00000001}) {
		t.Fatalf("pack = %08b", got)
	}
}

func writeBlankPNG(path string, w, h int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, image.NewNRGBA(image.Rect(0, 0, w, h)))
}
