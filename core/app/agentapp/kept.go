package agentapp

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/sihekuang/deskpixie/core/agent"
	agentfs "github.com/sihekuang/deskpixie/core/agent/fs"
)

// Kept is <app_data>/agent/kept.json: what a quit with keepPanesOnQuit left running, with its
// bearer tokens (0600; read once, at the next start, and deleted).
func (p Paths) Kept() string { return filepath.Join(p.Root, "kept.json") }

// KeptFile is the runtime's agent.KeptStore at that path.
type KeptFile string

var _ agent.KeptStore = KeptFile("")

func (k KeptFile) Save(kept agent.KeptAgents) error {
	b, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	return agentfs.WritePrivate(string(k), string(b))
}

// LoadKept reads the kept-agents file and deletes it. Missing or unreadable: nothing is kept.
func LoadKept(path string) agent.KeptAgents {
	b, err := os.ReadFile(path)
	if err != nil {
		return agent.KeptAgents{}
	}
	_ = os.Remove(path)
	var kept agent.KeptAgents
	if err := json.Unmarshal(b, &kept); err != nil {
		log.Printf("agent: ignored an unreadable kept-agents file")
		return agent.KeptAgents{}
	}
	return kept
}
