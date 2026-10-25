package agent

// The roster beside Pixie (#91): Pixie's agents in Herdr or tmux and the sessions you started
// with pix, with counts for the chip. The coordinator is listed first but never counted.

// RosterRow is one agent in the roster.
type RosterRow struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	Status string `json:"status"`
	// Kind: coordinator | worker | pane (in Pixie's session, no job owns it) | session (yours).
	Kind      string `json:"kind"`
	Reachable bool   `json:"reachable"`
	// App is a session's terminal app bundle id ("" = unknown: the row isn't clickable).
	App string `json:"app,omitempty"`
	// Stoppable: it is working and can be reached, so its row and its chat header show a
	// stop button (agent.interrupt, #309).
	Stoppable bool `json:"stoppable,omitempty"`
}

// RosterView is the whole roster. AgentCount excludes the coordinator; YourCount counts only
// reachable sessions. Busy: the coordinator, a worker or pane, or a reachable session of yours
// is working (the chip's busy dot, #234).
type RosterView struct {
	Agents     []RosterRow `json:"agents"`
	Yours      []RosterRow `json:"yours"`
	AgentCount int         `json:"agent_count"`
	YourCount  int         `json:"your_count"`
	Busy       bool        `json:"busy"`
}

// EmptyRoster has empty lists (never null in JSON).
func EmptyRoster() RosterView { return RosterView{Agents: []RosterRow{}, Yours: []RosterRow{}} }

// BuildRoster turns the combined host list into the roster; workers (the jobs' worker rows)
// tell workers from other panes; hostName ("herdr", "tmux") is the detail of those other panes.
// Label is how the session list names your session: its description, else "<tool> in <repo>",
// else its tool.
func (u *UserSession) Label() string {
	switch {
	case u.Description != "":
		return u.Description
	case u.Repo != "":
		return u.Tool + " in " + u.Repo
	}
	return u.Tool
}

func BuildRoster(list []AgentInfo, workers []WorkerRow, hostName string) RosterView {
	v := EmptyRoster()
	repos := map[string]string{}
	for _, w := range workers {
		repos[w.Name] = w.Repo
	}
	var coordinator []RosterRow
	for _, a := range list {
		switch {
		case a.Origin == OriginUser:
			if a.User == nil {
				continue
			}
			u := a.User
			v.Yours = append(v.Yours, RosterRow{Name: a.Name, Label: u.Label(), Detail: u.Where, Status: a.Status.String(),
				Kind: "session", Reachable: u.Reachable, App: u.App})
			if u.Reachable {
				v.YourCount++
			}
		case a.Name == Coordinator:
			coordinator = []RosterRow{{Name: a.Name, Label: "Coordinator", Status: a.Status.String(), Kind: "coordinator", Reachable: true}}
		default:
			row := RosterRow{Name: a.Name, Label: a.Name, Status: a.Status.String(), Kind: "pane", Detail: hostName, Reachable: true}
			if repo, ok := repos[a.Name]; ok {
				row.Kind, row.Detail = "worker", repo
			}
			v.Agents = append(v.Agents, row)
			v.AgentCount++
		}
	}
	v.Agents = append(coordinator, v.Agents...)
	if v.Agents == nil {
		v.Agents = []RosterRow{}
	}
	for _, rows := range [][]RosterRow{v.Agents, v.Yours} {
		for i := range rows {
			rows[i].Stoppable = rows[i].Reachable && rows[i].Status == PaneWorking.String()
		}
	}
	v.Busy = v.IsBusy()
	return v
}

// IsBusy: a row of Pixie's (the coordinator included) or a reachable session of yours is working.
func (v RosterView) IsBusy() bool {
	working := PaneWorking.String()
	for _, r := range v.Agents {
		if r.Status == working {
			return true
		}
	}
	for _, r := range v.Yours {
		if r.Reachable && r.Status == working {
			return true
		}
	}
	return false
}
