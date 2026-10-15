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
}

// RosterView is the whole roster. AgentCount excludes the coordinator; YourCount counts only
// reachable sessions.
type RosterView struct {
	Agents     []RosterRow `json:"agents"`
	Yours      []RosterRow `json:"yours"`
	AgentCount int         `json:"agent_count"`
	YourCount  int         `json:"your_count"`
}

// EmptyRoster has empty lists (never null in JSON).
func EmptyRoster() RosterView { return RosterView{Agents: []RosterRow{}, Yours: []RosterRow{}} }

// BuildRoster turns the combined host list into the roster; workers (the jobs' worker rows)
// tell workers from other panes; hostName ("herdr", "tmux") is the detail of those other panes.
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
			label := u.Description
			if label == "" && u.Repo != "" {
				label = u.Tool + " in " + u.Repo
			}
			if label == "" {
				label = u.Tool
			}
			v.Yours = append(v.Yours, RosterRow{Name: a.Name, Label: label, Detail: u.Where, Status: a.Status.String(),
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
	return v
}
