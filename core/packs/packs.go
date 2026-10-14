// Package packs finds mascot packs on disk (bundled + user folders), validates them and
// resolves them into the fully populated shape the Swift player reads. Port of ai-mascot
// src-tauri/src/packs.rs and crates/mascot-pack (manifest, validate, resolve), contract v1.
package packs

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png" // sheets are PNGs
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ContractVersion is the only mascot.json contract this package understands.
const ContractVersion = 1

// Limits, as in mascot-pack validate.rs.
const (
	MaxSheetSide  = 4096
	MaxSheetBytes = 16 * 1024 * 1024
	MaxBobPx      = 16
)

// StateNames is every state the app can ask for, in display order.
var StateNames = [...]string{"idle", "listening", "thinking", "delegating", "happy", "error"}

// RequiredStates must be present in every manifest.
var RequiredStates = [...]string{"idle", "listening", "thinking"}

// fallbacks: optional state -> the state it copies when absent.
var fallbacks = [...][2]string{{"delegating", "thinking"}, {"happy", "idle"}, {"error", "idle"}}

// Bob is the idle bob: px of vertical travel over frames.
type Bob struct {
	Px     uint32 `json:"px"`
	Frames uint32 `json:"frames"`
}

// State is one resolved animation row (Rust ResolvedState).
type State struct {
	Row    uint32  `json:"row"`
	Frames uint32  `json:"frames"`
	Loop   bool    `json:"loop"`
	Bob    bool    `json:"bob"`
	FaceDy []int32 `json:"faceDy"` // one entry per frame
}

// Mouth is the mouth overlay row.
type Mouth struct {
	Row    uint32    `json:"row"`
	Anchor [2]uint32 `json:"anchor"`
	Size   [2]uint32 `json:"size"`
	Levels uint32    `json:"levels"`
}

// Pack is a loaded, validated pack. Its JSON is exactly Rust's ResolvedPack.
type Pack struct {
	ID        string `json:"-"` // directory name
	Dir       string `json:"-"` // absolute pack directory
	SheetPath string `json:"-"` // absolute sheet path

	Name   string           `json:"name"`
	Cell   [2]uint32        `json:"cell"`
	FPS    uint32           `json:"fps"`
	Bob    Bob              `json:"bob"`
	States map[string]State `json:"states"` // every StateNames entry; encodes sorted like a BTreeMap
	Mouth  Mouth            `json:"mouth"`
}

// Load scans <root>/<id>/ in each root, in order. A later root replaces an earlier pack
// with the same id (bundled first, then user). Missing roots are skipped. The returned map
// always holds every valid pack; a non-nil error lists the packs that were rejected and is
// not fatal.
func Load(roots ...string) (map[string]Pack, error) {
	packs := map[string]Pack{}
	var rejected []error
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		var dirs []string
		for _, e := range entries {
			p := filepath.Join(root, e.Name())
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				dirs = append(dirs, p)
			}
		}
		sort.Strings(dirs)
		for _, dir := range dirs {
			p, err := loadPack(dir)
			if err != nil {
				rejected = append(rejected, fmt.Errorf("mascot pack %s: %w", dir, err))
				log.Printf("packs: rejected %s: %v", dir, err) // as log_rejected in packs.rs
				continue
			}
			packs[p.ID] = p
		}
	}
	return packs, errors.Join(rejected...)
}

// Select picks the wanted pack, else "bit", else the first valid pack by id.
func Select(packs map[string]Pack, wanted string) (Pack, bool) {
	if p, ok := packs[wanted]; ok {
		return p, true
	}
	if p, ok := packs["bit"]; ok {
		return p, true
	}
	if len(packs) == 0 {
		return Pack{}, false
	}
	ids := make([]string, 0, len(packs))
	for id := range packs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return packs[ids[0]], true
}

// --- manifest (mascot.json) ---

type manifest struct {
	Name     *string               `json:"name"`
	Contract *uint32               `json:"contract"`
	Cell     *[2]uint32            `json:"cell"`
	Sheet    *string               `json:"sheet"`
	FPS      *uint32               `json:"fps"`
	Bob      *bobSpec              `json:"bob"`
	States   *map[string]stateSpec `json:"states"`
	Mouth    *mouthSpec            `json:"mouth"`
}

type bobSpec struct {
	Px     *uint32 `json:"px"`
	Frames *uint32 `json:"frames"`
}

type stateSpec struct {
	Row    *uint32 `json:"row"`
	Frames *uint32 `json:"frames"`
	Loop   *bool   `json:"loop"`
	Bob    *bool   `json:"bob"`
	FaceDy []int32 `json:"faceDy"`
}

type mouthSpec struct {
	Row    *uint32    `json:"row"`
	Anchor *[2]uint32 `json:"anchor"`
	Size   *[2]uint32 `json:"size"`
	Levels *uint32    `json:"levels"`
}

// parseManifest decodes mascot.json, rejecting missing required fields (as serde does).
// Unknown fields are ignored.
func parseManifest(data []byte) (*manifest, error) {
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	var missing []string
	need := func(ok bool, name string) {
		if !ok {
			missing = append(missing, name)
		}
	}
	need(m.Name != nil, "name")
	need(m.Contract != nil, "contract")
	need(m.Cell != nil, "cell")
	need(m.Sheet != nil, "sheet")
	need(m.FPS != nil, "fps")
	need(m.States != nil, "states")
	need(m.Mouth != nil, "mouth")
	if m.Bob != nil {
		need(m.Bob.Px != nil, "bob.px")
		need(m.Bob.Frames != nil, "bob.frames")
	}
	if m.States != nil {
		for name, s := range *m.States {
			need(s.Row != nil, "states."+name+".row")
			need(s.Frames != nil, "states."+name+".frames")
		}
	}
	if m.Mouth != nil {
		need(m.Mouth.Row != nil, "mouth.row")
		need(m.Mouth.Anchor != nil, "mouth.anchor")
		need(m.Mouth.Size != nil, "mouth.size")
		need(m.Mouth.Levels != nil, "mouth.levels")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("missing field(s): %s", strings.Join(missing, ", "))
	}
	return &m, nil
}

// validate checks a parsed manifest against its sheet's pixel size (validate.rs).
func validate(m *manifest, sw, sh int) []string {
	var errs []string
	add := func(path, format string, a ...any) { errs = append(errs, path+": "+fmt.Sprintf(format, a...)) }
	cw, ch := uint64(m.Cell[0]), uint64(m.Cell[1])
	if *m.Contract != ContractVersion {
		add("contract", "unsupported contract %d (expected %d)", *m.Contract, ContractVersion)
	}
	if sw > MaxSheetSide || sh > MaxSheetSide {
		add("sheet", "%dx%d exceeds the %dx%d limit", sw, sh, MaxSheetSide, MaxSheetSide)
	}
	if cw == 0 || ch == 0 {
		add("cell", "cell width and height must be > 0")
		return errs
	}
	rows := uint64(sh) / ch
	if *m.FPS < 1 || *m.FPS > 60 {
		add("fps", "fps %d must be between 1 and 60", *m.FPS)
	}
	if m.Bob != nil {
		if *m.Bob.Frames == 0 {
			add("bob.frames", "must be > 0")
		}
		if *m.Bob.Px > MaxBobPx {
			add("bob.px", "%d must be at most %d", *m.Bob.Px, MaxBobPx)
		}
	}
	states := *m.States
	for _, name := range RequiredStates {
		if _, ok := states[name]; !ok {
			add("states."+name, "required state missing")
		}
	}
	names := make([]string, 0, len(states))
	for n := range states {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		s, p := states[name], "states."+name
		if *s.Frames == 0 {
			add(p+".frames", "must be > 0")
		}
		if uint64(*s.Row) >= rows {
			add(p+".row", "row %d exceeds sheet rows (%d)", *s.Row, rows)
		}
		if need := uint64(*s.Frames) * cw; need > uint64(sw) {
			add(p+".frames", "%d frames need %dpx width, sheet is %dpx", *s.Frames, need, sw)
		}
		if s.FaceDy != nil && len(s.FaceDy) != int(*s.Frames) {
			add(p+".faceDy", "has %d entries, expected %d (one per frame)", len(s.FaceDy), *s.Frames)
		}
	}
	mo := m.Mouth
	if *mo.Levels < 2 {
		add("mouth.levels", "levels %d must be >= 2", *mo.Levels)
	}
	if uint64(*mo.Row) >= rows {
		add("mouth.row", "row %d exceeds sheet rows (%d)", *mo.Row, rows)
	}
	if need := uint64(*mo.Levels) * cw; need > uint64(sw) {
		add("mouth.levels", "%d levels need %dpx width, sheet is %dpx", *mo.Levels, need, sw)
	}
	ex, ey := uint64(mo.Anchor[0])+uint64(mo.Size[0]), uint64(mo.Anchor[1])+uint64(mo.Size[1])
	if mo.Size[0] == 0 || mo.Size[1] == 0 || ex > cw || ey > ch {
		add("mouth.anchor", "anchor + size (%d, %d) outside cell (%d, %d)", ex, ey, cw, ch)
	}
	return errs
}

// loadPack reads <dir>/mascot.json, checks the sheet stays inside dir, reads the sheet's
// dimensions, validates and resolves. Never panics on bad input.
func loadPack(dir string) (Pack, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Pack{}, err
	}
	data, err := os.ReadFile(filepath.Join(abs, "mascot.json"))
	if err != nil {
		return Pack{}, fmt.Errorf("mascot.json: cannot read: %w", err)
	}
	m, err := parseManifest(data)
	if err != nil {
		return Pack{}, fmt.Errorf("mascot.json: %w", err)
	}
	if !insidePack(*m.Sheet) {
		return Pack{}, fmt.Errorf("sheet: '%s' must be a relative path inside the pack", *m.Sheet)
	}
	sheet := filepath.Join(abs, filepath.FromSlash(*m.Sheet))
	if st, err := os.Stat(sheet); err == nil && st.Size() > MaxSheetBytes {
		return Pack{}, fmt.Errorf("sheet: %s is larger than %d bytes", sheet, MaxSheetBytes)
	}
	f, err := os.Open(sheet)
	if err != nil {
		return Pack{}, fmt.Errorf("sheet: cannot read %s: %w", sheet, err)
	}
	cfg, _, err := image.DecodeConfig(f)
	_ = f.Close()
	if err != nil {
		return Pack{}, fmt.Errorf("sheet: cannot read %s: %w", sheet, err)
	}
	if errs := validate(m, cfg.Width, cfg.Height); len(errs) > 0 {
		return Pack{}, errors.New(strings.Join(errs, "; "))
	}
	p := resolve(m)
	p.ID = filepath.Base(abs)
	p.Dir = abs
	p.SheetPath = sheet
	return p, nil
}

// insidePack: non-empty, relative, and made only of normal components (no "..", ".", "/").
func insidePack(rel string) bool {
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return false
	}
	for _, c := range strings.Split(filepath.ToSlash(rel), "/") {
		if c == "" || c == "." || c == ".." {
			return false
		}
	}
	return true
}

// resolve fills defaults and fallbacks (resolve.rs). Precondition: m passed validate.
func resolve(m *manifest) Pack {
	states := map[string]State{}
	for _, name := range StateNames {
		s, ok := (*m.States)[name]
		if !ok {
			continue
		}
		st := State{Row: *s.Row, Frames: *s.Frames, Loop: true, Bob: true}
		if s.Loop != nil {
			st.Loop = *s.Loop
		}
		if s.Bob != nil {
			st.Bob = *s.Bob
		}
		if s.FaceDy != nil {
			st.FaceDy = append([]int32(nil), s.FaceDy...)
		} else {
			st.FaceDy = make([]int32, *s.Frames)
		}
		states[name] = st
	}
	for _, fb := range fallbacks {
		if _, ok := states[fb[0]]; !ok {
			t := states[fb[1]]
			t.FaceDy = append([]int32(nil), t.FaceDy...)
			states[fb[0]] = t
		}
	}
	bob := Bob{Px: 0, Frames: 1}
	if m.Bob != nil {
		bob = Bob{Px: *m.Bob.Px, Frames: *m.Bob.Frames}
	}
	return Pack{
		Name:   *m.Name,
		Cell:   *m.Cell,
		FPS:    *m.FPS,
		Bob:    bob,
		States: states,
		Mouth: Mouth{
			Row:    *m.Mouth.Row,
			Anchor: *m.Mouth.Anchor,
			Size:   *m.Mouth.Size,
			Levels: *m.Mouth.Levels,
		},
	}
}
