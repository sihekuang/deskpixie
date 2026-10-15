package agent

import "context"

// SecondaryHost is a session host for agents the primary didn't start: the sessions you start
// with pix (#89).
type SecondaryHost interface {
	SessionHost
	Owns(name string) bool
}

// CompositeHost is the primary host (Herdr or tmux) plus a secondary one, as one SessionHost:
// starts go to the primary, per-agent calls to whichever host owns the agent, and lists and
// events are merged. Focus and tab labels apply to the primary's agents only.
type CompositeHost struct {
	primary   SessionHost
	secondary SecondaryHost
}

func NewCompositeHost(primary SessionHost, secondary SecondaryHost) *CompositeHost {
	return &CompositeHost{primary: primary, secondary: secondary}
}

// Primary is the host that starts Pixie's agents.
func (c *CompositeHost) Primary() SessionHost { return c.primary }

// Secondary holds the sessions you started yourself.
func (c *CompositeHost) Secondary() SecondaryHost { return c.secondary }

func (c *CompositeHost) owner(name string) SessionHost {
	if c.secondary.Owns(name) {
		return c.secondary
	}
	return c.primary
}

func (c *CompositeHost) Name() string                            { return c.primary.Name() }
func (c *CompositeHost) EnsureSession(ctx context.Context) error { return c.primary.EnsureSession(ctx) }
func (c *CompositeHost) StartAgent(ctx context.Context, spec AgentSpec) (AgentInfo, error) {
	return c.primary.StartAgent(ctx, spec)
}
func (c *CompositeHost) ReplaceAgent(ctx context.Context, spec AgentSpec) (AgentInfo, error) {
	return c.primary.ReplaceAgent(ctx, spec)
}
func (c *CompositeHost) Prompt(ctx context.Context, agent, text string) error {
	return c.owner(agent).Prompt(ctx, agent, text)
}
func (c *CompositeHost) SendKeys(ctx context.Context, agent string, keys []string) error {
	return c.owner(agent).SendKeys(ctx, agent, keys)
}
func (c *CompositeHost) Read(ctx context.Context, agent string, lines uint16) (string, error) {
	return c.owner(agent).Read(ctx, agent, lines)
}
func (c *CompositeHost) Close(ctx context.Context, agent string) error {
	return c.owner(agent).Close(ctx, agent)
}
func (c *CompositeHost) CloseExact(ctx context.Context, agent, pane string) error {
	return c.owner(agent).CloseExact(ctx, agent, pane)
}

// List is the primary's agents, then the secondary's; a failing secondary never fails it.
func (c *CompositeHost) List(ctx context.Context) ([]AgentInfo, error) {
	out, err := c.primary.List(ctx)
	if err != nil {
		return nil, err
	}
	if more, err := c.secondary.List(ctx); err == nil {
		out = append(out, more...)
	}
	return out, nil
}

// Subscribe merges both hosts' events.
func (c *CompositeHost) Subscribe() <-chan PaneEvent {
	out := make(chan PaneEvent, 64)
	a, b := c.primary.Subscribe(), c.secondary.Subscribe()
	go func() {
		defer close(out)
		for a != nil || b != nil {
			select {
			case ev, ok := <-a:
				if !ok {
					a = nil
					continue
				}
				out <- ev
			case ev, ok := <-b:
				if !ok {
					b = nil
					continue
				}
				out <- ev
			}
		}
	}()
	return out
}

func (c *CompositeHost) AttachCommand() []string { return c.primary.AttachCommand() }

// Focus brings a primary agent's tab forward; your own sessions live in your terminal.
func (c *CompositeHost) Focus(ctx context.Context, agent string) error {
	if f, ok := c.primary.(Focuser); ok && !c.secondary.Owns(agent) {
		return f.Focus(ctx, agent)
	}
	return nil
}

// Relabel names a primary agent's tab; pix sets its own tab title.
func (c *CompositeHost) Relabel(ctx context.Context, agent string, tab TabLabel) error {
	if l, ok := c.primary.(Labeler); ok && !c.secondary.Owns(agent) {
		return l.Relabel(ctx, agent, tab)
	}
	return nil
}

// ReportStatus passes hook status to the primary (pix sessions report their own).
func (c *CompositeHost) ReportStatus(agent string, status PaneStatus) {
	if r, ok := c.primary.(StatusReporter); ok {
		r.ReportStatus(agent, status)
	}
}

// Shutdown stops both hosts (each one that has a Shutdown: Herdr's event pump, pix's socket).
func (c *CompositeHost) Shutdown() {
	for _, h := range []SessionHost{c.primary, c.secondary} {
		if s, ok := h.(interface{ Shutdown() }); ok {
			s.Shutdown()
		}
	}
}
