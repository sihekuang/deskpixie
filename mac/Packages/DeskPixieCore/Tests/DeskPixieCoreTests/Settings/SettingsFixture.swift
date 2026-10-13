import Foundation
@testable import DeskPixieCore

/// The Go core's `settings.Views` output for the sources Rust's layout tests use
/// (packs bit/dino, both providers, macos-vpio, herdr, warp/iterm, one mic, no speakers), and
/// `settings.Default()` as JSON. Generated from core/settings on 2026-10-13; regenerate if the
/// schema changes.
enum SettingsFixture {
    static let fieldsJSON = #"""
[
 {
  "path": "hotkey",
  "section": "Mascot",
  "label": "Voice hotkey",
  "help": "Starts or ends a conversation with Bit from any app.",
  "kind": {
   "type": "hotkey"
  },
  "apply": "live",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "pack",
  "section": "Mascot",
  "label": "Mascot",
  "help": "Which mascot pack is shown.",
  "kind": {
   "type": "choice",
   "options": [
    {
     "value": "bit",
     "label": "Bit",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "dino",
     "label": "Dino",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    }
   ]
  },
  "apply": "live",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "scale",
  "section": "Mascot",
  "label": "Size",
  "help": "Whole-number sizes keep the pixel art crisp.",
  "kind": {
   "type": "int",
   "min": 1,
   "max": 16,
   "menu": true
  },
  "apply": "live",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "proximityFade.enabled",
  "section": "Mascot",
  "label": "Fade near the pointer",
  "help": "Bit fades as the pointer comes close, so it never blocks your work.",
  "kind": {
   "type": "bool"
  },
  "apply": "live",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "proximityFade.radius",
  "section": "Mascot",
  "label": "Fade distance",
  "help": "How far from Bit, in points, the fading starts.",
  "kind": {
   "type": "int",
   "min": 0,
   "max": 1000,
   "menu": false
  },
  "apply": "live",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "voice.provider",
  "section": "Voice",
  "label": "Voice service",
  "help": "The realtime voice service Bit talks through.",
  "kind": {
   "type": "choice",
   "options": [
    {
     "value": "openai-realtime",
     "label": "OpenAI Realtime",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "gemini-live",
     "label": "Gemini Live",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    }
   ]
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "voice.audio",
  "section": "Voice",
  "label": "Audio",
  "help": "The microphone and speaker backend.",
  "kind": {
   "type": "choice",
   "options": [
    {
     "value": "macos-vpio",
     "label": "macOS voice processing",
     "platforms": [
      "macos"
     ]
    }
   ]
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "voice.model",
  "section": "Voice",
  "label": "Model",
  "help": "The voice model's name, e.g. gpt-realtime.",
  "kind": {
   "type": "text",
   "optional": false
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "voice.voice",
  "section": "Voice",
  "label": "Voice",
  "help": "The voice Bit speaks with, e.g. marin.",
  "kind": {
   "type": "text",
   "optional": false
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "voice.chimes",
  "section": "Voice",
  "label": "Chimes",
  "help": "A rising chime when Bit listens, a falling one when it stops.",
  "kind": {
   "type": "bool"
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "voice.noiseFilter",
  "section": "Voice",
  "label": "Noise filter",
  "help": "Cleans background noise (fans, typing, hum) from your mic on this Mac. With Hush it also turns quieter background voices down, like a TV.",
  "kind": {
   "type": "bool"
  },
  "apply": "live",
  "platforms": [
   "macos"
  ]
 },
 {
  "path": "voice.noiseModel",
  "section": "Voice",
  "label": "Noise model",
  "help": "Hush also turns quieter background voices down by up to 18 dB. DeepFilterNet3 cleans steady noise only.",
  "kind": {
   "type": "choice",
   "options": [
    {
     "value": "hush",
     "label": "Hush (noise and background voices)",
     "platforms": [
      "macos"
     ]
    },
    {
     "value": "dfn3",
     "label": "DeepFilterNet3 (steady noise only)",
     "platforms": [
      "macos"
     ]
    }
   ]
  },
  "apply": "restart",
  "platforms": [
   "macos"
  ]
 },
 {
  "path": "voice.inputDevice",
  "section": "Voice",
  "label": "Microphone",
  "help": "The mic Bit listens with. If it isn't connected, Bit uses the system default until it is. Bluetooth mics are slow to start and can cut off your first words.",
  "kind": {
   "type": "choice",
   "options": [
    {
     "value": "",
     "label": "System Default",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "BuiltInMic",
     "label": "MacBook Pro Microphone",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    }
   ]
  },
  "apply": "live",
  "platforms": [
   "macos"
  ]
 },
 {
  "path": "voice.outputDevice",
  "section": "Voice",
  "label": "Speaker",
  "help": "Where Bit's voice plays. If it isn't connected, Bit uses the system default until it is.",
  "kind": {
   "type": "choice",
   "options": [
    {
     "value": "",
     "label": "System Default",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    }
   ]
  },
  "apply": "live",
  "platforms": [
   "macos"
  ]
 },
 {
  "path": "voice.noisyRoom",
  "section": "Voice",
  "label": "Noisy room",
  "help": "Bit ignores quieter voices in the background, like a TV or people nearby. Speak up a little to interrupt Bit.",
  "kind": {
   "type": "bool"
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "voice.idleTimeoutSecs",
  "section": "Voice",
  "label": "Silence timeout (seconds)",
  "help": "A conversation ends after this much silence.",
  "kind": {
   "type": "int",
   "min": 1,
   "max": 600,
   "menu": false
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "voice.homeCity",
  "section": "Voice",
  "label": "Home city",
  "help": "Where Bit checks the weather when you don't name a place, e.g. Portland, Oregon.",
  "kind": {
   "type": "text",
   "optional": true
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "voice.personaPath",
  "section": "Voice",
  "label": "Persona file",
  "help": "A Markdown file that replaces Bit's personality. Empty uses the built-in one.",
  "kind": {
   "type": "path",
   "dir": false,
   "optional": true
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.enabled",
  "section": "Agent",
  "label": "Delegation",
  "help": "Let Bit hand work to a Claude Code agent.",
  "kind": {
   "type": "bool"
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.terminal",
  "section": "Agent",
  "label": "Terminal",
  "help": "Where the agent session opens when you click Bit.",
  "kind": {
   "type": "choice",
   "options": [
    {
     "value": "warp",
     "label": "Warp",
     "platforms": [
      "macos"
     ]
    },
    {
     "value": "iterm",
     "label": "iTerm",
     "platforms": [
      "macos"
     ]
    }
   ]
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.model",
  "section": "Agent",
  "label": "Agent model",
  "help": "Claude Code's --model for the coordinator. Empty uses its default.",
  "kind": {
   "type": "text",
   "optional": true
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.warm",
  "section": "Agent",
  "label": "Start at launch",
  "help": "Start the coordinator when the app starts, not on first use.",
  "kind": {
   "type": "bool"
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.narration",
  "section": "Agent",
  "label": "Narration",
  "help": "Bit says what the agent is doing.",
  "kind": {
   "type": "bool"
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.holdOpenSecs",
  "section": "Agent",
  "label": "Hold open (seconds)",
  "help": "How long a conversation stays open while a job runs.",
  "kind": {
   "type": "int",
   "min": 0,
   "max": 3600,
   "menu": false
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.projectsRoot",
  "section": "Agent",
  "label": "Projects folder",
  "help": "The folder whose repositories workers may use. It must be inside your home folder.",
  "kind": {
   "type": "path",
   "dir": true,
   "optional": false
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.workers.enabled",
  "section": "Workers",
  "label": "Workers",
  "help": "Let the agent start worker agents in git worktrees.",
  "kind": {
   "type": "bool"
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.workers.max",
  "section": "Workers",
  "label": "Most workers at once",
  "help": "Up to four workers run at the same time.",
  "kind": {
   "type": "int",
   "min": 1,
   "max": 4,
   "menu": true
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.workers.kinds",
  "section": "Workers",
  "label": "Worker kinds",
  "help": "Which agents may work as workers.",
  "kind": {
   "type": "multiChoice",
   "options": [
    {
     "value": "claude",
     "label": "Claude Code",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "codex",
     "label": "Codex",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    }
   ]
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "canvas.autoOpen",
  "section": "Canvas",
  "label": "Open new pages",
  "help": "Show new and changed canvas pages automatically.",
  "kind": {
   "type": "bool"
  },
  "apply": "live",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "canvas.keep",
  "section": "Canvas",
  "label": "Pages kept",
  "help": "Older pages are deleted at startup.",
  "kind": {
   "type": "int",
   "min": 1,
   "max": 1000,
   "menu": false
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.sessionHost",
  "section": "Advanced",
  "label": "Session host",
  "help": "The terminal multiplexer that runs the agent.",
  "kind": {
   "type": "choice",
   "options": [
    {
     "value": "herdr",
     "label": "Herdr",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    }
   ]
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.session",
  "section": "Advanced",
  "label": "Session name",
  "help": "The Herdr session's name: a-z, 0-9 and -, not \"default\".",
  "kind": {
   "type": "text",
   "optional": false
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.herdrPath",
  "section": "Advanced",
  "label": "Herdr program",
  "help": "Empty finds herdr on PATH or in ~/.local/bin.",
  "kind": {
   "type": "path",
   "dir": false,
   "optional": true
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.port",
  "section": "Advanced",
  "label": "Hook server port",
  "help": "The local port agents report to.",
  "kind": {
   "type": "int",
   "min": 1024,
   "max": 65535,
   "menu": false
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.keepPanesOnQuit",
  "section": "Advanced",
  "label": "Keep agent panes on quit",
  "help": "Leave the agent's terminal panes open when the app quits.",
  "kind": {
   "type": "bool"
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.permissionTimeoutSecs",
  "section": "Advanced",
  "label": "Permission timeout (seconds)",
  "help": "An unanswered permission request is denied after this long.",
  "kind": {
   "type": "int",
   "min": 1,
   "max": 120,
   "menu": false
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 },
 {
  "path": "agent.notifyPerMinute",
  "section": "Advanced",
  "label": "Notifications per minute",
  "help": "The most agent notifications shown in one minute.",
  "kind": {
   "type": "int",
   "min": 1,
   "max": 60,
   "menu": false
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ]
 }
]
"""#

    static let defaultsJSON = #"""
{"hotkey": "ctrl+alt+Space", "pack": "bit", "scale": 8, "position": null, "proximityFade": {"enabled": true, "radius": 150}, "voice": {"provider": "openai-realtime", "audio": "macos-vpio", "model": "gpt-realtime", "voice": "marin", "chimes": true, "idleTimeoutSecs": 7, "personaPath": null, "noiseFilter": false, "noiseModel": "hush", "noisyRoom": false, "inputDevice": null, "outputDevice": null, "homeCity": null}, "canvas": {"autoOpen": true, "keep": 50, "window": null}, "agent": {"enabled": true, "sessionHost": "herdr", "session": "deskpixie", "terminal": "warp", "herdrPath": null, "model": null, "warm": false, "port": 48217, "keepPanesOnQuit": false, "holdOpenSecs": 180, "permissionTimeoutSecs": 120, "notifyPerMinute": 4, "narration": true, "projectsRoot": "~/Documents/Projects", "workers": {"enabled": false, "max": 4, "kinds": ["claude", "codex"]}}}
"""#

    static let fields: [SettingsField] = try! JSONDecoder().decode([SettingsField].self, from: Data(fieldsJSON.utf8))
    static let defaults: JSONValue = try! JSONDecoder().decode(JSONValue.self, from: Data(defaultsJSON.utf8))

    static func view(_ path: String) -> SettingsField {
        fields.first { $0.path == path }!
    }

    static func spec(_ path: String, _ values: JSONValue) -> SettingsLayout.ControlSpec {
        SettingsLayout.controlSpec(view(path), values: values, platform: .macos)
    }

    static func snapshot(seq: UInt64 = 1, values: JSONValue = defaults) -> SettingsSnapshot {
        SettingsSnapshot(platform: .macos, values: values, fields: fields, seq: seq)
    }
}
