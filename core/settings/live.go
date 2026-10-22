package settings

// Route is the app code path that applies a Live field: the same path the tray takes. P14's
// Applier switches on RouteFor(field).
type Route int

const (
	// RouteHotkey: Swift binds the shortcut (the `hotkey` event); on failure the old one stays.
	RouteHotkey Route = iota + 1
	// RoutePack: Tray ▸ Pixie.
	RoutePack
	// RouteScale: Tray ▸ Size.
	RouteScale
	// RouteReadEachTick: the proximity loop reads proximityFade on every tick: nothing to do.
	RouteReadEachTick
	// RouteAutoOpen: the canvas controller's auto-open switch.
	RouteAutoOpen
	// RouteResetPosition: Tray ▸ Reset position.
	RouteResetPosition
	// RouteNoiseFilter: the mic path reads the noise filter's switch on every frame.
	RouteNoiseFilter
	// RouteAudioDevices: the audio unit reads the chosen mic and speaker when the next
	// conversation builds it (the `devices` event).
	RouteAudioDevices
	// RouteNotifications: the updates bubble and presence monitor read these when they next
	// decide (the settings snapshot the app already holds).
	RouteNotifications
	// RouteReplyMode: the agent and the shell switch between voice and text (#221).
	RouteReplyMode
	// RouteCaptions: the shell keeps captions up or shows them on hover (#224).
	RouteCaptions
)

// RouteFor is the route for a Live field (or "position" for Reset position); ok is false for
// a Restart field.
func RouteFor(path string) (Route, bool) {
	switch path {
	case "hotkey":
		return RouteHotkey, true
	case "pack":
		return RoutePack, true
	case "scale":
		return RouteScale, true
	case "proximityFade.enabled", "proximityFade.radius", "logMaxMB":
		return RouteReadEachTick, true
	case "canvas.autoOpen":
		return RouteAutoOpen, true
	case "voice.noiseFilter":
		return RouteNoiseFilter, true
	case "voice.inputDevice", "voice.outputDevice":
		return RouteAudioDevices, true
	case "notifications.bubble.enabled", "notifications.bubble.popSecs", "notifications.bubble.lines",
		"notifications.awayAfterMins":
		return RouteNotifications, true
	case "replyMode":
		return RouteReplyMode, true
	case "alwaysShowCaption", "captionDismissSecs", "captionFadeSecs", "replyHistoryMins":
		return RouteCaptions, true
	case "position":
		return RouteResetPosition, true
	}
	return 0, false
}
