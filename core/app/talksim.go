package app

// Tray ▸ Simulate talking: fake mouth levels at ~30 Hz while it is on. While it is off the
// goroutine blocks on its switch channel: no polling, no app lock. While a voice conversation is
// open the voice loop owns the mouth: the simulator closes it once and stays quiet until the
// conversation ends. Port of ai-mascot src-tauri/src/talk_sim.rs.

import "time"

// TalkTick is ~30 Hz.
const TalkTick = 33 * time.Millisecond

// MouthPort is the simulator's outside world.
type MouthPort interface {
	// Levels is how many mouth levels the current pack has (read once per tick while talking).
	Levels() uint32
	// VoiceActive: a voice conversation is open (read once per tick while talking).
	VoiceActive() bool
	Mouth(level uint32)
}

// RunTalkSim follows sw (true = talking) until it is closed. It sends a level only when it
// changes, and level 0 when talking stops.
func RunTalkSim(sw <-chan bool, port MouthPort, tickEvery time.Duration) {
	var (
		tick    uint64
		last    uint32
		talking bool
	)
	ticker := time.NewTicker(tickEvery)
	defer ticker.Stop()
	for {
		var (
			on      bool
			message bool
		)
		if talking {
			select {
			case v, ok := <-sw:
				if !ok {
					return
				}
				on, message = v, true
			case <-ticker.C:
			}
		} else {
			v, ok := <-sw
			if !ok {
				return
			}
			on, message = v, true
		}
		var level uint32
		switch {
		case message:
			talking = on
			if on {
				ticker.Reset(tickEvery)
				continue
			}
			level = 0
		case port.VoiceActive():
			level = 0
		default:
			tick++
			level = TalkLevel(tick, port.Levels())
		}
		if level != last {
			port.Mouth(level)
			last = level
		}
	}
}
