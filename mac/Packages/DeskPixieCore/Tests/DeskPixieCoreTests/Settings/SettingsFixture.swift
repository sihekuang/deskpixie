import Foundation
@testable import DeskPixieCore

/// The Go core's `settings.Views` output for the sources Rust's layout tests use
/// (packs bit/dino, both providers with openai-realtime current, macos-vpio, herdr/tmux, warp/iterm,
/// one mic, no speakers), and
/// `settings.Default()` as JSON. Generated from core/settings on 2026-10-13; pages, groups, labels, help, units
/// and rules updated from it on 2026-10-24 (#290). Regenerate if the schema changes.
enum SettingsFixture {
    static let fieldsJSON = #"""
[
 {
  "path": "hotkey",
  "section": "General",
  "group": "Talking to Pixie",
  "label": "Voice hotkey",
  "help": "Starts or ends a conversation with Pixie from any app.",
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
  "section": "General",
  "group": "Look",
  "label": "Character",
  "help": "Which pixie is shown.",
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
  "section": "General",
  "group": "Look",
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
  "section": "General",
  "group": "Look",
  "label": "Fade near the pointer",
  "help": "Pixie fades as the pointer comes close, so it never blocks your work.",
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
  "section": "General",
  "group": "Look",
  "label": "Fade distance",
  "help": "How far from Pixie the fading starts.",
  "kind": {
   "type": "int",
   "min": 0,
   "max": 1000,
   "menu": false,
   "unit": "points"
  },
  "apply": "live",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ],
  "enabledWhen": [
   {
    "key": "proximityFade.enabled",
    "in": [
     "true"
    ]
   }
  ]
 },
 {
  "path": "voice.provider",
  "section": "Voice",
  "group": "Voice service",
  "label": "Voice service",
  "help": "The realtime voice service Pixie talks through.",
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
  "path": "voice.model",
  "section": "Voice",
  "group": "Voice service",
  "label": "Model",
  "help": "The voice model. The choices follow the voice service.",
  "kind": {
   "type": "choice",
   "options": [
    {
     "value": "gpt-realtime",
     "label": "GPT Realtime",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "gpt-realtime-mini",
     "label": "GPT Realtime mini",
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
  "path": "voice.voice",
  "section": "Voice",
  "group": "Voice service",
  "label": "Voice",
  "help": "The voice Pixie speaks with. The choices follow the voice service.",
  "kind": {
   "type": "choice",
   "options": [
    {
     "value": "alloy",
     "label": "Alloy",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "ash",
     "label": "Ash",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "ballad",
     "label": "Ballad",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "cedar",
     "label": "Cedar",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "coral",
     "label": "Coral",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "echo",
     "label": "Echo",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "marin",
     "label": "Marin",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "sage",
     "label": "Sage",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "shimmer",
     "label": "Shimmer",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "verse",
     "label": "Verse",
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
  "path": "voice.inputDevice",
  "section": "Voice",
  "group": "Microphone and speaker",
  "label": "Microphone",
  "help": "The mic Pixie listens with. If it isn't connected, Pixie uses the system default until it is. Bluetooth mics are slow to start and can cut off your first words.",
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
  "group": "Microphone and speaker",
  "label": "Speaker",
  "help": "Where Pixie's voice plays. If it isn't connected, Pixie uses the system default until it is.",
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
  "path": "voice.noiseFilter",
  "section": "Voice",
  "group": "Microphone and speaker",
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
  "group": "Microphone and speaker",
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
  ],
  "enabledWhen": [
   {
    "key": "voice.noiseFilter",
    "in": [
     "true"
    ]
   }
  ]
 },
 {
  "path": "voice.noisyRoom",
  "section": "Voice",
  "group": "Microphone and speaker",
  "label": "Noisy room",
  "help": "Pixie ignores quieter voices in the background, like a TV or people nearby. Speak up a little to interrupt Pixie. With OpenAI GPT-Live this only changes Pixie's instructions.",
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
  "path": "voice.chimes",
  "section": "Voice",
  "group": "Microphone and speaker",
  "label": "Chimes",
  "help": "A rising chime when Pixie listens, a falling one when it stops.",
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
  "group": "Conversation",
  "label": "Silence timeout",
  "help": "A conversation ends after this much silence.",
  "kind": {
   "type": "int",
   "min": 1,
   "max": 600,
   "menu": false,
   "unit": "seconds"
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
  "group": "Conversation",
  "label": "Home city",
  "help": "Where Pixie checks the weather when you don't name a place, e.g. Portland, Oregon.",
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
  "path": "agent.enabled",
  "section": "Coordinator",
  "group": "Coordinator",
  "label": "Hand work to the coordinator",
  "help": "Pixie hands work it can't do itself to the coordinator, a Claude Code or Codex agent.",
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
  "path": "agent.model",
  "section": "Coordinator",
  "group": "Coordinator",
  "label": "Coordinator model",
  "help": "Claude Code's --model for the coordinator (and Claude workers). Default lets Claude Code decide.",
  "kind": {
   "type": "choice",
   "options": [
    {
     "value": "",
     "label": "Default (Claude Code decides)",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "opus",
     "label": "Opus (latest)",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "sonnet",
     "label": "Sonnet (latest)",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "haiku",
     "label": "Haiku (latest)",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "fable",
     "label": "Fable (latest)",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "claude-opus-5-5",
     "label": "Claude Opus 5.5",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "claude-sonnet-5-5",
     "label": "Claude Sonnet 5.5",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "claude-haiku-5-5",
     "label": "Claude Haiku 5.5",
     "platforms": [
      "macos",
      "windows",
      "linux"
     ]
    },
    {
     "value": "claude-fable-5-1",
     "label": "Claude Fable 5.1",
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
  ],
  "visibleWhen": [
   {
    "key": "agent.coordinatorKind",
    "in": [
     "claude"
    ]
   }
  ],
  "enabledWhen": [
   {
    "key": "agent.enabled",
    "in": [
     "true"
    ]
   }
  ]
 },
 {
  "path": "agent.warm",
  "section": "Coordinator",
  "group": "Coordinator",
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
  ],
  "enabledWhen": [
   {
    "key": "agent.enabled",
    "in": [
     "true"
    ]
   }
  ]
 },
 {
  "path": "agent.terminal",
  "section": "Coordinator",
  "group": "Coordinator",
  "label": "Terminal",
  "help": "Where the coordinator's session opens when you click Pixie.",
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
  ],
  "enabledWhen": [
   {
    "key": "agent.enabled",
    "in": [
     "true"
    ]
   }
  ]
 },
 {
  "path": "agent.holdOpenSecs",
  "section": "Coordinator",
  "group": "Handing work off",
  "label": "Keep listening while a job runs",
  "help": "How long a conversation stays open while the coordinator works on something you asked.",
  "kind": {
   "type": "int",
   "min": 0,
   "max": 3600,
   "menu": false,
   "unit": "seconds"
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ],
  "enabledWhen": [
   {
    "key": "agent.enabled",
    "in": [
     "true"
    ]
   }
  ]
 },
 {
  "path": "agent.narration",
  "section": "Coordinator",
  "group": "Updates",
  "label": "Say what the coordinator is doing",
  "help": "Pixie tells you what the coordinator is up to while it works.",
  "kind": {
   "type": "bool"
  },
  "apply": "restart",
  "platforms": [
   "macos",
   "windows",
   "linux"
  ],
  "enabledWhen": [
   {
    "key": "agent.enabled",
    "in": [
     "true"
    ]
   }
  ]
 },
 {
  "path": "canvas.autoOpen",
  "section": "Coordinator",
  "group": "Canvas",
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
  "section": "Coordinator",
  "group": "Canvas",
  "label": "Pages kept",
  "help": "Older pages are deleted at startup.",
  "kind": {
   "type": "int",
   "min": 1,
   "max": 1000,
   "menu": false,
   "unit": "pages"
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
  "group": "Workers",
  "label": "Workers",
  "help": "Let the coordinator start worker agents in git worktrees.",
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
  "group": "Workers",
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
  ],
  "enabledWhen": [
   {
    "key": "agent.workers.enabled",
    "in": [
     "true"
    ]
   }
  ]
 },
 {
  "path": "agent.workers.kinds",
  "section": "Workers",
  "group": "Workers",
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
  ],
  "enabledWhen": [
   {
    "key": "agent.workers.enabled",
    "in": [
     "true"
    ]
   }
  ]
 },
 {
  "path": "agent.projectsRoot",
  "section": "Workers",
  "group": "Workers",
  "label": "Projects folder",
  "help": "The folder whose repositories workers may use, inside your home folder. DeskPixie creates it when it's missing.",
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
  ],
  "enabledWhen": [
   {
    "key": "agent.workers.enabled",
    "in": [
     "true"
    ]
   }
  ]
 },
 {
  "path": "agent.sessionHost",
  "section": "Advanced",
  "group": "Session host",
  "label": "Session host",
  "help": "The terminal multiplexer that runs the coordinator. Supported: Herdr 0.7.5 (protocol 17) or 0.9.x (protocol 22), or tmux 3.2 or newer.",
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
    },
    {
     "value": "tmux",
     "label": "tmux",
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
  "group": "Session host",
  "label": "Session name",
  "help": "The coordinator session's name: a-z, 0-9 and -, not \"default\".",
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
  "group": "Session host",
  "label": "Herdr program",
  "help": "Empty finds herdr on PATH or in ~/.local/bin, /opt/homebrew/bin or /usr/local/bin. Supported: Herdr 0.7.5 (protocol 17) or 0.9.x (protocol 22).",
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
  "path": "agent.tmuxPath",
  "section": "Advanced",
  "group": "Session host",
  "label": "tmux program",
  "help": "Empty finds tmux on PATH or in /opt/homebrew/bin or /usr/local/bin. Supported: tmux 3.2 or newer.",
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
  "path": "agent.keepPanesOnQuit",
  "section": "Advanced",
  "group": "Session host",
  "label": "Keep agent panes on quit",
  "help": "Leave the coordinator's and workers' terminal panes open when the app quits.",
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
  "path": "agent.port",
  "section": "Advanced",
  "group": "Coordinator",
  "label": "Agent report port",
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
  "path": "agent.permissionTimeoutSecs",
  "section": "Advanced",
  "group": "Coordinator",
  "label": "Permission timeout",
  "help": "An unanswered permission request is denied after this long.",
  "kind": {
   "type": "int",
   "min": 1,
   "max": 120,
   "menu": false,
   "unit": "seconds"
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
  "group": "Coordinator",
  "label": "Most coordinator notifications",
  "help": "The most coordinator notifications shown in one minute.",
  "kind": {
   "type": "int",
   "min": 1,
   "max": 60,
   "menu": false,
   "unit": "a minute"
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
  "section": "Advanced",
  "group": "Voice",
  "label": "Audio engine",
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
  "path": "voice.personaPath",
  "section": "Advanced",
  "group": "Voice",
  "label": "Persona file",
  "help": "A Markdown file that replaces Pixie's personality. Empty uses the built-in one.",
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
 }
]
"""#

    static let defaultsJSON = #"""
{"hotkey": "ctrl+alt+Space", "pack": "bit", "scale": 8, "position": null, "proximityFade": {"enabled": false, "radius": 150}, "voice": {"provider": "openai-realtime", "audio": "macos-vpio", "model": "gpt-realtime", "voice": "marin", "chimes": true, "idleTimeoutSecs": 7, "personaPath": null, "noiseFilter": false, "noiseModel": "hush", "noisyRoom": false, "inputDevice": null, "outputDevice": null, "homeCity": null}, "canvas": {"autoOpen": true, "keep": 50, "window": null}, "agent": {"enabled": true, "sessionHost": "herdr", "session": "deskpixie", "terminal": "warp", "herdrPath": null, "tmuxPath": null, "model": null, "warm": false, "port": 48217, "keepPanesOnQuit": true, "holdOpenSecs": 180, "permissionTimeoutSecs": 120, "notifyPerMinute": 4, "narration": true, "projectsRoot": "~/Documents/deskpixie", "workers": {"enabled": false, "max": 4, "kinds": ["claude", "codex"]}}}
"""#

    /// Go `ExtraViews(Macos)`.
    static let extrasJSON = #"""
[{"id": "resetPosition", "section": "General", "group": "Look"}, {"id": "key.openai", "section": "Voice", "group": "Voice service"}, {"id": "key.gemini", "section": "Voice", "group": "Voice service"}, {"id": "restartCoordinator", "section": "Coordinator", "group": "Coordinator"}, {"id": "pix", "section": "Sessions", "group": "Sessions you start with pix"}, {"id": "openFile", "section": "Advanced", "group": "Files"}, {"id": "exportLogs", "section": "Advanced", "group": "Files"}]
"""#

    static let extras: [SettingsExtraPlace] = try! JSONDecoder().decode([SettingsExtraPlace].self, from: Data(extrasJSON.utf8))
    static let fields: [SettingsField] = try! JSONDecoder().decode([SettingsField].self, from: Data(fieldsJSON.utf8))
    static let defaults: JSONValue = try! JSONDecoder().decode(JSONValue.self, from: Data(defaultsJSON.utf8))

    /// The extra rows the core places on `section`, in order.
    static func extras(on section: SettingsSection) -> [SettingsLayout.Extra] {
        SettingsLayout.groups(in: section, fields: [], extras: extras).flatMap(\.rows).compactMap {
            if case .extra(let e) = $0 { return e }
            return nil
        }
    }

    static func view(_ path: String) -> SettingsField {
        fields.first { $0.path == path }!
    }

    static func spec(_ path: String, _ values: JSONValue) -> SettingsLayout.ControlSpec {
        SettingsLayout.controlSpec(view(path), values: values, platform: .macos)
    }

    static func snapshot(seq: UInt64 = 1, values: JSONValue = defaults) -> SettingsSnapshot {
        SettingsSnapshot(platform: .macos, values: values, fields: fields, extras: extras, seq: seq)
    }
}
