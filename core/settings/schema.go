package settings

import (
	"encoding/json"
	"runtime"
	"strings"

	"github.com/sihekuang/deskpixie/core/herdr/compat"
	tmuxcompat "github.com/sihekuang/deskpixie/core/tmux/compat"
)

// Platform is a platform the Settings window styles itself for.
type Platform string

const (
	Macos   Platform = "macos"
	Windows Platform = "windows"
	Linux   Platform = "linux"
)

// AllPlatforms is every platform, in Rust's order.
var AllPlatforms = []Platform{Macos, Windows, Linux}

var mac = []Platform{Macos}

// CurrentPlatform is this build's platform. Anything that is not macOS or Windows is Linux.
func CurrentPlatform() Platform {
	switch runtime.GOOS {
	case "darwin":
		return Macos
	case "windows":
		return Windows
	}
	return Linux
}

// MaxWorkers is the most workers at once.
const MaxWorkers = 4

// Section is a Settings window tab. It serializes as its Rust name ("Pixie", …).
type Section string

const (
	SectionPixie         Section = "Pixie"
	SectionVoice         Section = "Voice"
	SectionAgent         Section = "Agent"
	SectionWorkers       Section = "Workers"
	SectionNotifications Section = "Notifications"
	SectionCanvas        Section = "Canvas"
	SectionAdvanced      Section = "Advanced"
)

// Apply says when a change takes effect.
type Apply string

const (
	// ApplyLive: the running app follows the change at once (the Applier).
	ApplyLive Apply = "live"
	// ApplyRestart: read once at startup; the window shows "Restart to apply".
	ApplyRestart Apply = "restart"
)

// Opt is one choice: the stored value, its label, and the platforms it applies to.
type Opt struct {
	Value     string
	Label     string
	Platforms []Platform
}

// ChoiceSource is where a choice field's options come from.
type ChoiceSource int

const (
	SourceStatic ChoiceSource = iota
	SourcePacks
	SourceVoiceProviders
	SourceAudioBackends
	SourceSessionHosts
	SourceTerminals
	// SourceInputDevices: connected microphones, after "System Default" (stored as null).
	SourceInputDevices
	// SourceOutputDevices: connected speakers, after "System Default" (stored as null).
	SourceOutputDevices
	// SourceVoiceModels: the models of the current voice.provider (OptionSources.VoiceProvider).
	SourceVoiceModels
	// SourceVoiceVoices: the voices of the current voice.provider.
	SourceVoiceVoices
)

// Choices is a choice field's option source; Static holds the options for SourceStatic.
type Choices struct {
	Source ChoiceSource
	Static []Opt
	// Open marks suggested choices: any text is still a valid value (the voice adapter
	// substitutes what it can't use, Claude Code checks its --model), so a value not listed
	// is kept and shown "(not found)" rather than refused.
	Open bool
	// Null: an option with the value "" stands for null (agent.model's "Default").
	Null bool
}

// Nullable reports whether null is a value: the first option ("System Default", "Default")
// stands for it.
func (c Choices) Nullable() bool {
	return c.Null || c.Source == SourceInputDevices || c.Source == SourceOutputDevices
}

// KindType is a field's control kind.
type KindType int

const (
	KindBool KindType = iota
	// KindInt: a whole number in Min..=Max; Menu shows every value in a pop-up menu.
	KindInt
	KindChoice
	KindMultiChoice
	// KindText: Optional means empty text is stored as null.
	KindText
	KindPath
	KindHotkey
	// KindManaged: state the app writes itself (window geometry): no control.
	KindManaged
)

// Kind is a field's kind with its parameters.
type Kind struct {
	Type     KindType
	Min, Max int64
	Menu     bool
	Choices  Choices
	Optional bool
	Dir      bool
}

// FieldSpec describes one leaf of settings.json.
type FieldSpec struct {
	// Path is the dotted camelCase path, e.g. agent.workers.max.
	Path      string
	Section   Section
	Label     string
	Help      string
	Kind      Kind
	Apply     Apply
	Platforms []Platform
	// VisibleWhen: the field shows only while every condition holds (nil: always shown).
	// A hidden field keeps its stored value.
	VisibleWhen []Condition
}

// Condition holds while the field at Key has one of the values In. Values compare as the
// window shows them: a string as itself, a bool as "true"/"false", a number in decimal.
type Condition struct {
	Key string   `json:"key"`
	In  []string `json:"in"`
}

// visibleWhen adds conditions to f; all of them must hold.
func visibleWhen(f FieldSpec, conds ...Condition) FieldSpec {
	f.VisibleWhen = append(append([]Condition{}, f.VisibleWhen...), conds...)
	return f
}

// gptLiveOnly is the condition that the voice service is OpenAI GPT-Live.
var gptLiveOnly = Condition{Key: "voice.provider", In: []string{ProviderOpenAILive}}

func spec(path string, section Section, label, help string, kind Kind, apply Apply) FieldSpec {
	return FieldSpec{Path: path, Section: section, Label: label, Help: help, Kind: kind, Apply: apply, Platforms: AllPlatforms}
}

func macOnly(f FieldSpec) FieldSpec {
	f.Platforms = mac
	return f
}

var (
	boolKind    = Kind{Type: KindBool}
	hotkeyKind  = Kind{Type: KindHotkey}
	managedKind = Kind{Type: KindManaged}
)

func intKind(min, max int64, menu bool) Kind {
	return Kind{Type: KindInt, Min: min, Max: max, Menu: menu}
}
func textKind(optional bool) Kind      { return Kind{Type: KindText, Optional: optional} }
func pathKind(dir, optional bool) Kind { return Kind{Type: KindPath, Dir: dir, Optional: optional} }
func choiceKind(src ChoiceSource) Kind { return Kind{Type: KindChoice, Choices: Choices{Source: src}} }

// openChoice is a choice whose options are suggestions (Choices.Open).
func openChoice(c Choices) Kind {
	c.Open = true
	return Kind{Type: KindChoice, Choices: c}
}
func staticChoice(opts []Opt) Kind {
	return Kind{Type: KindChoice, Choices: Choices{Source: SourceStatic, Static: opts}}
}

// NoiseModels are the noise filter's models.
var NoiseModels = []Opt{
	{"hush", "Hush (noise and background voices)", mac},
	{"dfn3", "DeepFilterNet3 (steady noise only)", mac},
}

// GPT-Live delegation modes (voice.liveDelegation).
const (
	LiveDelegationBackend = "backend"
	LiveDelegationAgent   = "agent"
)

// LiveDelegations are voice.liveDelegation's choices.
var LiveDelegations = []Opt{
	{LiveDelegationBackend, "OpenAI backend model", AllPlatforms},
	{LiveDelegationAgent, "Straight to the coordinator (experimental)", AllPlatforms},
}

// Suggested voice models and voices per provider. The Gemini lists mirror core/voice/gemini
// (DefaultModel, the legacy preview its docs name, and Voices); a test keeps them in step.
var (
	OpenAIModels = []Opt{
		{"gpt-realtime", "GPT Realtime", AllPlatforms},
		{"gpt-realtime-mini", "GPT Realtime mini", AllPlatforms},
	}
	GeminiModels = []Opt{
		{"gemini-3.8-live", "Gemini 3.8 Live", AllPlatforms},
		{"gemini-3.1-flash-live-preview", "Gemini 3.1 Flash Live (preview)", AllPlatforms},
	}
	// OpenAILiveModels / OpenAILiveVoices mirror core/voice/openailive (DefaultModel, Voices).
	OpenAILiveModels = []Opt{{"gpt-live-1", "GPT-Live 1", AllPlatforms}}
	OpenAILiveVoices = titled(
		"alloy", "ash", "ballad", "beacon", "bossa", "brise", "cedar", "cinder", "coral", "delta",
		"echo", "flitz", "gleam", "harema", "juni", "marin", "meridian", "nira", "noeul", "nuri",
		"quartz", "ripple", "sage", "shida", "shimmer", "sillage", "stone", "tempo", "verse",
		"vesper", "willow",
	)
	// LiveBackendModels are suggested Responses models for GPT-Live's tools.
	LiveBackendModels = []Opt{
		{"gpt-6-luna", "GPT-6 Luna (faster, cheaper)", AllPlatforms},
		{"gpt-6-sol", "GPT-6 Sol (harder tasks)", AllPlatforms},
	}
	OpenAIVoices = titled("alloy", "ash", "ballad", "cedar", "coral", "echo", "marin", "sage", "shimmer", "verse")
	GeminiVoices = titled(
		"Zephyr", "Puck", "Charon", "Kore", "Fenrir", "Leda", "Orus", "Aoede", "Callirrhoe", "Autonoe",
		"Enceladus", "Iapetus", "Umbriel", "Algieba", "Despina", "Erinome", "Algenib", "Rasalgethi",
		"Laomedeia", "Achernar", "Alnilam", "Schedar", "Gacrux", "Pulcherrima", "Achird",
		"Zubenelgenubi", "Vindemiatrix", "Sadachbia", "Sadaltager", "Sulafat",
	)
)

// ProviderDefault is a voice provider's default model and voice.
type ProviderDefault struct{ Model, Voice string }

// ProviderDefaults: switching voice.provider swaps one provider's default for the other's.
var ProviderDefaults = map[string]ProviderDefault{
	ProviderOpenAI:     {"gpt-realtime", "marin"},
	ProviderGemini:     {"gemini-3.8-live", "Puck"},
	ProviderOpenAILive: {"gpt-live-1", "marin"},
}

// titled is one option per name, labelled with a capital first letter.
func titled(names ...string) []Opt {
	out := make([]Opt, 0, len(names))
	for _, n := range names {
		out = append(out, Opt{n, strings.ToUpper(n[:1]) + n[1:], AllPlatforms})
	}
	return out
}

// AgentModels are suggested values of Claude Code's --model: "" (null) leaves it to Claude
// Code, then the aliases `claude --help` documents, then full model ids.
var AgentModels = []Opt{
	{"", "Default (Claude Code decides)", AllPlatforms},
	{"opus", "Opus (latest)", AllPlatforms},
	{"sonnet", "Sonnet (latest)", AllPlatforms},
	{"haiku", "Haiku (latest)", AllPlatforms},
	{"fable", "Fable (latest)", AllPlatforms},
	{"claude-opus-5-5", "Claude Opus 5.5", AllPlatforms},
	{"claude-sonnet-5-5", "Claude Sonnet 5.5", AllPlatforms},
	{"claude-haiku-5-5", "Claude Haiku 5.5", AllPlatforms},
	{"claude-fable-5-1", "Claude Fable 5.1", AllPlatforms},
}

// Coordinator kinds (agent.coordinatorKind).
const (
	CoordinatorClaude = "claude"
	CoordinatorCodex  = "codex"
)

// CoordinatorKinds are the agents that may be the coordinator.
var CoordinatorKinds = []Opt{
	{CoordinatorClaude, "Claude Code", AllPlatforms},
	{CoordinatorCodex, "Codex", AllPlatforms},
}

// CodexModels are suggested values of Codex's -m for the coordinator: "" (null) leaves it to
// Codex, then the GPT models Codex 0.162 lists.
var CodexModels = []Opt{
	{"", "Default (Codex decides)", AllPlatforms},
	{"gpt-6.1-sol", "GPT-6.1 Sol", AllPlatforms},
	{"gpt-6-astra", "GPT-6 Astra", AllPlatforms},
	{"gpt-6-sol", "GPT-6 Sol", AllPlatforms},
	{"gpt-6-luna", "GPT-6 Luna", AllPlatforms},
	{"gpt-5.6-sol", "GPT-5.6 Sol", AllPlatforms},
	{"gpt-5.6-terra", "GPT-5.6 Terra", AllPlatforms},
	{"gpt-5.6-luna", "GPT-5.6 Luna", AllPlatforms},
}

// claudeCoordinator / codexCoordinator: the coordinator is that agent.
var (
	claudeCoordinator = Condition{Key: "agent.coordinatorKind", In: []string{CoordinatorClaude}}
	codexCoordinator  = Condition{Key: "agent.coordinatorKind", In: []string{CoordinatorCodex}}
)

// WorkerKinds are the agents that may work as workers.
var WorkerKinds = []Opt{
	{"claude", "Claude Code", AllPlatforms},
	{"codex", "Codex", AllPlatforms},
}

// Provider registry names (the voice registry is P14's; these are the names it registers).
const (
	ProviderOpenAI = "openai-realtime"
	ProviderGemini = "gemini-live"
	// ProviderOpenAILive is OpenAI GPT-Live (core/voice/openailive).
	ProviderOpenAILive = "openai-live"
)

// Labels and platforms for registry names, per registry. A registered name not listed shows as
// itself, on every platform. A listed name this build did not register is still offered,
// available nowhere, so a current value reads "(not available on this platform)" rather than
// "(not found)".
var (
	providerNames = []Opt{
		{ProviderOpenAI, "OpenAI Realtime", AllPlatforms},
		{ProviderGemini, "Gemini Live", AllPlatforms},
		{ProviderOpenAILive, "OpenAI GPT-Live", AllPlatforms},
	}
	audioNames    = []Opt{{"macos-vpio", "macOS voice processing", mac}}
	hostNames     = []Opt{{"herdr", "Herdr", AllPlatforms}, {"tmux", "tmux", AllPlatforms}}
	terminalNames = []Opt{{"warp", "Warp", mac}, {"iterm", "iTerm", mac}}
)

// Fields is every leaf of Config, in page order within each section. A test fails when a
// Config leaf has no entry.
var Fields = []FieldSpec{
	spec("hotkey", SectionPixie, "Voice hotkey", "Starts or ends a conversation with Pixie from any app.", hotkeyKind, ApplyLive),
	spec("pack", SectionPixie, "Pixie", "Which pixie pack is shown.", choiceKind(SourcePacks), ApplyLive),
	spec("scale", SectionPixie, "Size", "Whole-number sizes keep the pixel art crisp.", intKind(1, MaxScale, true), ApplyLive),
	spec("proximityFade.enabled", SectionPixie, "Fade near the pointer", "Pixie fades as the pointer comes close, so it never blocks your work.", boolKind, ApplyLive),
	spec("proximityFade.radius", SectionPixie, "Fade distance", "How far from Pixie, in points, the fading starts.", intKind(0, 1000, false), ApplyLive),
	spec("position", SectionPixie, "Position", "Where Pixie was last dragged; written by the app.", managedKind, ApplyLive),
	spec("voice.provider", SectionVoice, "Voice service", "The realtime voice service Pixie talks through.", choiceKind(SourceVoiceProviders), ApplyRestart),
	// The GPT-Live fields sit right under the voice service and show only for GPT-Live.
	visibleWhen(spec("voice.liveDelegation", SectionVoice, "GPT-Live delegation", "OpenAI backend model: a backend model runs Pixie's tools (time, weather, timers, music) and hands other work to the coordinator. Straight to the coordinator: GPT-Live hands everything it can't answer to the coordinator, with no backend model.", staticChoice(LiveDelegations), ApplyRestart), gptLiveOnly),
	// The backend model is unused when GPT-Live delegates straight to the coordinator.
	visibleWhen(spec("voice.liveBackendModel", SectionVoice, "Backend model (GPT-Live)", "The model that runs Pixie's tools while GPT-Live talks.", openChoice(Choices{Source: SourceStatic, Static: LiveBackendModels}), ApplyRestart),
		gptLiveOnly, Condition{Key: "voice.liveDelegation", In: []string{LiveDelegationBackend}}),
	spec("voice.audio", SectionVoice, "Audio", "The microphone and speaker backend.", choiceKind(SourceAudioBackends), ApplyRestart),
	// voice.model, voice.voice and agent.model are free Text in Rust; here they are open
	// choices (suggestions; any text still validates) — a deliberate deviation (#45).
	spec("voice.model", SectionVoice, "Model", "The voice model. The choices follow the voice service.", openChoice(Choices{Source: SourceVoiceModels}), ApplyRestart),
	spec("voice.voice", SectionVoice, "Voice", "The voice Pixie speaks with. The choices follow the voice service.", openChoice(Choices{Source: SourceVoiceVoices}), ApplyRestart),
	spec("voice.chimes", SectionVoice, "Chimes", "A rising chime when Pixie listens, a falling one when it stops.", boolKind, ApplyRestart),
	macOnly(spec("voice.noiseFilter", SectionVoice, "Noise filter", "Cleans background noise (fans, typing, hum) from your mic on this Mac. With Hush it also turns quieter background voices down, like a TV.", boolKind, ApplyLive)),
	macOnly(spec("voice.noiseModel", SectionVoice, "Noise model", "Hush also turns quieter background voices down by up to 18 dB. DeepFilterNet3 cleans steady noise only.", staticChoice(NoiseModels), ApplyRestart)),
	macOnly(spec("voice.inputDevice", SectionVoice, "Microphone", "The mic Pixie listens with. If it isn't connected, Pixie uses the system default until it is. Bluetooth mics are slow to start and can cut off your first words.", choiceKind(SourceInputDevices), ApplyLive)),
	macOnly(spec("voice.outputDevice", SectionVoice, "Speaker", "Where Pixie's voice plays. If it isn't connected, Pixie uses the system default until it is.", choiceKind(SourceOutputDevices), ApplyLive)),
	spec("voice.noisyRoom", SectionVoice, "Noisy room", "Pixie ignores quieter voices in the background, like a TV or people nearby. Speak up a little to interrupt Pixie. With OpenAI GPT-Live this only changes Pixie's instructions.", boolKind, ApplyRestart),
	spec("voice.handOffWhenUnsure", SectionVoice, "Hand off when Pixie doesn't know", "When Pixie can't answer a question or request itself, it hands it to the coordinator instead of saying it doesn't know.", boolKind, ApplyRestart),
	spec("voice.checkClaims", SectionVoice, "Check Pixie's claims", "When Pixie says it did something, like setting a timer or handing a task off, the app checks it really did and corrects it if not.", boolKind, ApplyRestart),
	spec("voice.idleTimeoutSecs", SectionVoice, "Silence timeout (seconds)", "A conversation ends after this much silence.", intKind(1, 600, false), ApplyRestart),
	spec("voice.homeCity", SectionVoice, "Home city", "Where Pixie checks the weather when you don't name a place, e.g. Portland, Oregon.", textKind(true), ApplyRestart),
	spec("voice.personaPath", SectionVoice, "Persona file", "A Markdown file that replaces Pixie's personality. Empty uses the built-in one.", pathKind(false, true), ApplyRestart),
	spec("agent.enabled", SectionAgent, "Delegation", "Let Pixie hand work to the coordinator, a Claude Code or Codex agent.", boolKind, ApplyRestart),
	spec("agent.terminal", SectionAgent, "Terminal", "Where the coordinator's session opens when you click Pixie.", choiceKind(SourceTerminals), ApplyRestart),
	spec("agent.coordinatorKind", SectionAgent, "Coordinator agent", "The agent that runs as the coordinator: Claude Code, or OpenAI's Codex.", staticChoice(CoordinatorKinds), ApplyRestart),
	// The model list follows the coordinator agent. agent.model also applies to Claude workers.
	visibleWhen(spec("agent.model", SectionAgent, "Coordinator model", "Claude Code's --model for the coordinator (and Claude workers). Default lets Claude Code decide.", openChoice(Choices{Source: SourceStatic, Static: AgentModels, Null: true}), ApplyRestart), claudeCoordinator),
	visibleWhen(spec("agent.codexModel", SectionAgent, "Coordinator model", "Codex's model for the coordinator. Default lets Codex decide.", openChoice(Choices{Source: SourceStatic, Static: CodexModels, Null: true}), ApplyRestart), codexCoordinator),
	spec("agent.warm", SectionAgent, "Start at launch", "Start the coordinator when the app starts, not on first use.", boolKind, ApplyRestart),
	spec("agent.narration", SectionAgent, "Narration", "Pixie says what the coordinator is doing.", boolKind, ApplyRestart),
	spec("agent.openConversationForUpdates", SectionAgent, "Open a conversation for updates", "When a job finishes or fails, or the coordinator asks you something, while you're not talking to Pixie, Pixie starts a conversation to tell you and then listens for your answer.", boolKind, ApplyRestart),
	spec("agent.holdOpenSecs", SectionAgent, "Hold open (seconds)", "How long a conversation stays open while a job runs.", intKind(0, 3600, false), ApplyRestart),
	spec("agent.projectsRoot", SectionAgent, "Projects folder", "The folder whose repositories workers may use. It must be inside your home folder.", pathKind(true, false), ApplyRestart),
	spec("agent.workers.enabled", SectionWorkers, "Workers", "Let the coordinator start worker agents in git worktrees.", boolKind, ApplyRestart),
	spec("agent.workers.max", SectionWorkers, "Most workers at once", "Up to four workers run at the same time.", intKind(1, MaxWorkers, true), ApplyRestart),
	spec("agent.workers.kinds", SectionWorkers, "Worker kinds", "Which agents may work as workers.", Kind{Type: KindMultiChoice, Choices: Choices{Source: SourceStatic, Static: WorkerKinds}}, ApplyRestart),
	spec("agent.workers.speakResults", SectionWorkers, "Speak worker reports", "Pixie tells you as soon as a worker says it's done, couldn't finish or is stuck, before the coordinator has checked its work.", boolKind, ApplyRestart),
	spec("canvas.autoOpen", SectionCanvas, "Open new pages", "Show new and changed canvas pages automatically.", boolKind, ApplyLive),
	spec("canvas.keep", SectionCanvas, "Pages kept", "Older pages are deleted at startup.", intKind(1, 1000, false), ApplyRestart),
	spec("canvas.window", SectionCanvas, "Canvas window", "The canvas window's last place and size; written by the app.", managedKind, ApplyLive),
	spec("agent.sessionHost", SectionAdvanced, "Session host", "The terminal multiplexer that runs the coordinator. Supported: "+compat.SupportedVersions+", or "+tmuxcompat.SupportedVersions+".", choiceKind(SourceSessionHosts), ApplyRestart),
	spec("agent.session", SectionAdvanced, "Session name", "The coordinator session's name: a-z, 0-9 and -, not \"default\".", textKind(false), ApplyRestart),
	spec("agent.herdrPath", SectionAdvanced, "Herdr program", "Empty finds herdr on PATH or in ~/.local/bin, /opt/homebrew/bin or /usr/local/bin. Supported: "+compat.SupportedVersions+".", pathKind(false, true), ApplyRestart),
	spec("agent.tmuxPath", SectionAdvanced, "tmux program", "Empty finds tmux on PATH or in /opt/homebrew/bin or /usr/local/bin. Supported: "+tmuxcompat.SupportedVersions+".", pathKind(false, true), ApplyRestart),
	spec("agent.port", SectionAdvanced, "Hook server port", "The local port agents report to.", intKind(1024, 65535, false), ApplyRestart),
	spec("agent.keepPanesOnQuit", SectionAdvanced, "Keep agent panes on quit", "Leave the coordinator's and workers' terminal panes open when the app quits.", boolKind, ApplyRestart),
	spec("agent.permissionTimeoutSecs", SectionAdvanced, "Permission timeout (seconds)", "An unanswered permission request is denied after this long.", intKind(1, 120, false), ApplyRestart),
	spec("agent.notifyPerMinute", SectionNotifications, "Notifications per minute", "The most coordinator notifications shown in one minute.", intKind(1, 60, false), ApplyRestart),
	spec("notifications.debounceSecs", SectionNotifications, "Notification debounce (seconds)", "A job's updates wait until it has been quiet this long, then go out together. 0 sends each one as it comes. Questions never wait.", intKind(0, 300, false), ApplyRestart),
	spec("notifications.bubble.enabled", SectionNotifications, "Updates bubble", "Shows what happened since you last talked to Pixie in a bubble beside it.", boolKind, ApplyLive),
	spec("notifications.bubble.popSecs", SectionNotifications, "Pop for (seconds)", "How long the bubble shows a new update before it shrinks to a badge.", intKind(1, 60, false), ApplyLive),
	spec("notifications.bubble.lines", SectionNotifications, "Updates shown", "The most updates the bubble lists.", intKind(1, 10, false), ApplyLive),
	spec("notifications.awayAfterMins", SectionNotifications, "Away after (minutes idle)", "With no keyboard or mouse input for this long, nothing pops and Pixie doesn't start a conversation until you're back.", intKind(1, 60, false), ApplyLive),
}

// FieldByPath is the field at path, or nil.
func FieldByPath(path string) *FieldSpec {
	for i := range Fields {
		if Fields[i].Path == path {
			return &Fields[i]
		}
	}
	return nil
}

// LivePaths are the fields the running app applies at once.
func LivePaths() []string {
	var out []string
	for _, f := range Fields {
		if f.Apply == ApplyLive && f.Kind.Type != KindManaged {
			out = append(out, f.Path)
		}
	}
	return out
}

// Pair is a runtime option: (id or UID, display name).
type Pair struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// OptionSources are the runtime choices: the loaded packs and the registries' names.
type OptionSources struct {
	Packs     []Pair
	Providers []string
	Audio     []string
	Hosts     []string
	Terminals []string
	// Inputs are microphones as (UID, label), read fresh for each snapshot (WithSaved).
	Inputs []Pair
	// Outputs are speakers as (UID, label), like Inputs.
	Outputs []Pair
	// VoiceProvider is the current voice.provider; the model and voice choices follow it.
	// Snapshot fills it from the config.
	VoiceProvider string
}

// The value and label that stand for null in a device choice, and the labels of a saved
// device that isn't connected.
const (
	SystemDefaultLabel = "System Default"
	MissingMic         = "Saved microphone — not connected"
	MissingSpeaker     = "Saved speaker — not connected"
)

// WithSaved is the connected devices plus the saved one when it isn't connected, labelled
// missing, so the menu still shows it (selected) rather than "(not found)".
func WithSaved(listed []Pair, saved *string, missing string) []Pair {
	out := append([]Pair{}, listed...)
	if saved == nil || *saved == "" {
		return out
	}
	for _, p := range out {
		if p.Value == *saved {
			return out
		}
	}
	return append(out, Pair{*saved, missing})
}

// OptionView is one option as the window sees it.
type OptionView struct {
	Value     string     `json:"value"`
	Label     string     `json:"label"`
	Platforms []Platform `json:"platforms"`
}

func viewOf(o Opt) OptionView {
	return OptionView{Value: o.Value, Label: o.Label, Platforms: append([]Platform{}, o.Platforms...)}
}

func named(known []Opt, registered []string) []OptionView {
	out := []OptionView{}
	for _, name := range registered {
		v := OptionView{Value: name, Label: name, Platforms: append([]Platform{}, AllPlatforms...)}
		for _, k := range known {
			if k.Value == name {
				v = viewOf(k)
			}
		}
		out = append(out, v)
	}
	for _, k := range known {
		found := false
		for _, r := range registered {
			found = found || r == k.Value
		}
		if !found {
			out = append(out, OptionView{Value: k.Value, Label: k.Label, Platforms: []Platform{}})
		}
	}
	return out
}

// Options is every option of choices, on every platform; the window and Validate filter by
// platform.
func Options(c Choices, src OptionSources) []OptionView {
	switch c.Source {
	case SourceStatic:
		return static(c.Static)
	case SourcePacks:
		out := []OptionView{}
		for _, p := range src.Packs {
			out = append(out, OptionView{Value: p.Value, Label: p.Label, Platforms: append([]Platform{}, AllPlatforms...)})
		}
		return out
	case SourceVoiceProviders:
		return named(providerNames, src.Providers)
	case SourceAudioBackends:
		return named(audioNames, src.Audio)
	case SourceSessionHosts:
		return named(hostNames, src.Hosts)
	case SourceTerminals:
		return named(terminalNames, src.Terminals)
	case SourceInputDevices:
		return devices(src.Inputs)
	case SourceOutputDevices:
		return devices(src.Outputs)
	case SourceVoiceModels:
		return static(map[string][]Opt{ProviderOpenAI: OpenAIModels, ProviderGemini: GeminiModels, ProviderOpenAILive: OpenAILiveModels}[src.VoiceProvider])
	case SourceVoiceVoices:
		return static(map[string][]Opt{ProviderOpenAI: OpenAIVoices, ProviderGemini: GeminiVoices, ProviderOpenAILive: OpenAILiveVoices}[src.VoiceProvider])
	}
	return []OptionView{}
}

func static(opts []Opt) []OptionView {
	out := []OptionView{}
	for _, o := range opts {
		out = append(out, viewOf(o))
	}
	return out
}

// devices is "System Default", then each device.
func devices(listed []Pair) []OptionView {
	all := func() []Platform { return append([]Platform{}, AllPlatforms...) }
	out := []OptionView{{Value: "", Label: SystemDefaultLabel, Platforms: all()}}
	for _, p := range listed {
		out = append(out, OptionView{Value: p.Value, Label: p.Label, Platforms: all()})
	}
	return out
}

// KindView is a field's control kind as the window sees it, options resolved. It serializes
// with a "type" tag: bool, int{min,max,menu}, choice{options}, multiChoice{options},
// text{optional}, path{dir,optional}, hotkey.
type KindView struct {
	Type     KindType
	Min, Max int64
	Menu     bool
	Options  []OptionView
	Optional bool
	Dir      bool
}

// MarshalJSON writes the tagged form.
func (k KindView) MarshalJSON() ([]byte, error) {
	switch k.Type {
	case KindBool:
		return json.Marshal(map[string]any{"type": "bool"})
	case KindInt:
		return json.Marshal(struct {
			Type string `json:"type"`
			Min  int64  `json:"min"`
			Max  int64  `json:"max"`
			Menu bool   `json:"menu"`
		}{"int", k.Min, k.Max, k.Menu})
	case KindChoice, KindMultiChoice:
		name := "choice"
		if k.Type == KindMultiChoice {
			name = "multiChoice"
		}
		opts := k.Options
		if opts == nil {
			opts = []OptionView{}
		}
		return json.Marshal(struct {
			Type    string       `json:"type"`
			Options []OptionView `json:"options"`
		}{name, opts})
	case KindText:
		return json.Marshal(struct {
			Type     string `json:"type"`
			Optional bool   `json:"optional"`
		}{"text", k.Optional})
	case KindPath:
		return json.Marshal(struct {
			Type     string `json:"type"`
			Dir      bool   `json:"dir"`
			Optional bool   `json:"optional"`
		}{"path", k.Dir, k.Optional})
	case KindHotkey:
		return json.Marshal(map[string]any{"type": "hotkey"})
	}
	return nil, &json.UnsupportedValueError{Str: "managed field has no view"}
}

// FieldView is one field for the window.
type FieldView struct {
	Path      string     `json:"path"`
	Section   Section    `json:"section"`
	Label     string     `json:"label"`
	Help      string     `json:"help"`
	Kind      KindView   `json:"kind"`
	Apply     Apply      `json:"apply"`
	Platforms []Platform `json:"platforms"`
	// VisibleWhen is left out when the field always shows.
	VisibleWhen []Condition `json:"visibleWhen,omitempty"`
}

// Views is the schema for the window: every field with a control, options resolved from src.
func Views(src OptionSources) []FieldView {
	out := []FieldView{}
	for _, f := range Fields {
		if f.Kind.Type == KindManaged {
			continue
		}
		k := KindView{Type: f.Kind.Type, Min: f.Kind.Min, Max: f.Kind.Max, Menu: f.Kind.Menu, Optional: f.Kind.Optional, Dir: f.Kind.Dir}
		if f.Kind.Type == KindChoice || f.Kind.Type == KindMultiChoice {
			k.Options = Options(f.Kind.Choices, src)
		}
		out = append(out, FieldView{
			Path: f.Path, Section: f.Section, Label: f.Label, Help: f.Help, Kind: k, Apply: f.Apply,
			Platforms:   append([]Platform{}, f.Platforms...),
			VisibleWhen: conditionsView(f.VisibleWhen),
		})
	}
	return out
}

// conditionsView copies conds for a view; nil stays nil so the JSON leaves it out.
func conditionsView(conds []Condition) []Condition {
	if len(conds) == 0 {
		return nil
	}
	out := make([]Condition, len(conds))
	for i, c := range conds {
		out[i] = Condition{Key: c.Key, In: append([]string{}, c.In...)}
	}
	return out
}
