package config

// Kind is how a key's value is written and read.
type Kind int

const (
	KindDuration Kind = iota // Go form, such as 750ms or 2m
	KindSize                 // bytes, such as 768KiB
	KindRate                 // bytes per second, such as 256KiB/s
	KindFloat
	KindInt
	KindBool
)

// Check is the range a value must fall in.
type Check int

const (
	Positive    Check = iota // > 0
	NonNegative              // >= 0
	Factor                   // >= 1
	Fraction                 // 0 to 1
	Alpha                    // above 0, at most 1
	Any
	Base64Part // at least 4 bytes: one base64 group, which carries 3
)

// Key is one setting. Slow is its value in the built-in slow profile, or
// empty when slow keeps the default.
type Key struct {
	Name    string
	Kind    Kind
	Default string
	Slow    string
	Check   Check
	// Restart marks a key roard reads only when it starts.
	Restart bool
	// Host marks a key that a [hosts.<label>] section may set: it applies
	// to talking to one host.
	Host bool
	Doc  string
}

// SlowValue is the key's value in the slow profile.
func (k Key) SlowValue() string {
	if k.Slow == "" {
		return k.Default
	}
	return k.Slow
}

// keys is every setting, in the order docs/ROARD.md lists them.
var keys = []Key{
	{Name: "sender.connect.timeout", Kind: KindDuration, Default: "10s", Slow: "45s", Host: true, Doc: "Opening the WebSocket and getting the welcome"},
	{Name: "sender.connect.initial", Kind: KindDuration, Default: "500ms", Slow: "2s", Host: true, Doc: "Reconnect backoff: first delay"},
	{Name: "sender.connect.factor", Kind: KindFloat, Default: "2", Check: Factor, Host: true, Doc: "Reconnect backoff: growth per try"},
	{Name: "sender.connect.max", Kind: KindDuration, Default: "30s", Slow: "2m", Host: true, Doc: "Reconnect backoff: longest delay"},
	{Name: "sender.connect.jitter", Kind: KindFloat, Default: "0.5", Check: Fraction, Host: true, Doc: "Reconnect backoff: random share of each delay"},
	{Name: "sender.connect.reset_after", Kind: KindDuration, Default: "60s", Slow: "5m", Host: true, Doc: "Reconnect backoff: a connection this old starts over"},
	{Name: "sender.call.timeout", Kind: KindDuration, Default: "10s", Slow: "30s", Host: true, Doc: "Floor of each call's deadline"},
	{Name: "sender.call.rtt_factor", Kind: KindFloat, Default: "8", Slow: "12", Check: Factor, Host: true, Doc: "Deadline as a multiple of the smoothed round trip"},
	{Name: "sender.call.rtt_alpha", Kind: KindFloat, Default: "0.125", Check: Alpha, Host: true, Doc: "How fast the smoothed round trip follows new samples"},
	{Name: "sender.call.remote_timeout", Kind: KindDuration, Default: "10s", Host: true, Doc: "timeout_ms passed to Rex: roard's own time to answer once a call reaches it, at most 30s"},
	{Name: "sender.stall_timeout", Kind: KindDuration, Default: "45s", Slow: "3m", Host: true, Doc: "No message on the connection for this long, while a call waits, means reconnect"},
	{Name: "sender.print_deadline", Kind: KindDuration, Default: "15s", Slow: "60s", Host: true, Doc: "Print the latest state after this"},
	{Name: "sender.transfer.base", Kind: KindDuration, Default: "2m", Slow: "10m", Host: true, Doc: "A whole send's deadline before upload time"},
	{Name: "sender.transfer.min_rate", Kind: KindRate, Default: "256KiB/s", Slow: "16KiB/s", Host: true, Doc: "Upload time added to the deadline; each acknowledged part extends it"},
	{Name: "sender.upload.part_size", Kind: KindSize, Default: "768KiB", Slow: "128KiB", Check: Base64Part, Host: true, Doc: "Base64 bytes per part, capped by the receiver's max_part"},
	{Name: "sender.upload.in_flight", Kind: KindInt, Default: "1", Host: true, Doc: "Parts sent before the first reply"},

	{Name: "roard.rex.reconnect.initial", Kind: KindDuration, Default: "1s", Doc: "Backoff to the local Rex server: first delay"},
	{Name: "roard.rex.reconnect.factor", Kind: KindFloat, Default: "2", Check: Factor, Doc: "Backoff to the local Rex server: growth per try"},
	{Name: "roard.rex.reconnect.max", Kind: KindDuration, Default: "30s", Doc: "Backoff to the local Rex server: longest delay"},
	{Name: "roard.rex.reconnect.jitter", Kind: KindFloat, Default: "0.2", Check: Fraction, Doc: "Backoff to the local Rex server: random share"},
	{Name: "roard.rex.reconnect.reset_after", Kind: KindDuration, Default: "60s", Doc: "Backoff to the local Rex server: a connection this old starts over"},
	{Name: "roard.callback.timeout", Kind: KindDuration, Default: "10s", Slow: "30s", Doc: "Floor of each roar.result call's deadline"},
	{Name: "roard.callback.rtt_factor", Kind: KindFloat, Default: "8", Slow: "12", Check: Factor, Doc: "Callback deadline as a multiple of that sender's smoothed round trip"},
	{Name: "roard.callback.initial", Kind: KindDuration, Default: "250ms", Slow: "1s", Doc: "Callback backoff: first delay"},
	{Name: "roard.callback.factor", Kind: KindFloat, Default: "2", Check: Factor, Doc: "Callback backoff: growth per try"},
	{Name: "roard.callback.max", Kind: KindDuration, Default: "10s", Slow: "1m", Doc: "Callback backoff: longest delay"},
	{Name: "roard.callback.jitter", Kind: KindFloat, Default: "0.5", Check: Fraction, Doc: "Callback backoff: random share"},
	{Name: "roard.callback.give_up", Kind: KindDuration, Default: "2m", Slow: "10m", Doc: "Stop calling back; the sender's resubscribe still finds the record"},
	{Name: "roard.callback.max_timeout", Kind: KindDuration, Default: "30s", Doc: "Cap on a sender's callback_timeout hint"},
	{Name: "roard.callback.max_give_up", Kind: KindDuration, Default: "15m", Slow: "30m", Doc: "Cap on a sender's callback_give_up hint"},
	{Name: "roard.workers", Kind: KindInt, Default: "4", Restart: true, Doc: "Worker count"},
	{Name: "roard.upload.max_workers", Kind: KindInt, Default: "2", Doc: "Workers one upload may hold"},
	{Name: "roard.upload.max_count", Kind: KindInt, Default: "8", Doc: "Open uploads per host"},
	{Name: "roard.upload.max_bytes", Kind: KindSize, Default: "64MiB", Doc: "Bytes reserved by open uploads per host"},
	{Name: "roard.upload.idle_expiry", Kind: KindDuration, Default: "10m", Slow: "1h", Doc: "Delete an upload with no part for this long"},
	{Name: "roard.upload.max_age", Kind: KindDuration, Default: "1h", Slow: "6h", Doc: "Delete an upload this old"},
	{Name: "roard.handler.warn", Kind: KindDuration, Default: "200ms", Doc: "Log a handler slower than this"},
	{Name: "roard.log.max_size", Kind: KindSize, Default: "1MiB", Doc: "Log rotation"},
	{Name: "roard.relay.retire", Kind: KindBool, Default: "true", Check: Any, Doc: "Close and destroy roar-relay"},
	{Name: "roard.service.restart_delay", Kind: KindDuration, Default: "2s", Restart: true, Doc: "RestartSec and launchd's throttle; applied by roar setup"},

	{Name: "watcher.coalesce", Kind: KindDuration, Default: "50ms", Check: NonNegative, Doc: "Events for one pane within this window cause one attempt"},
	{Name: "watcher.gap_recheck", Kind: KindDuration, Default: "30s", Doc: "Re-check only for pending work that no event covers"},
	{Name: "watcher.sweep", Kind: KindDuration, Default: "60s", Doc: "Backstop while anything is queued, for a wake-up that never came"},
	{Name: "watcher.legacy_lock_retry", Kind: KindDuration, Default: "5s", Doc: "Only while an old watcher holds watch.lock"},

	{Name: "install.deadline", Kind: KindDuration, Default: "2m", Slow: "10m", Host: true, Doc: "A whole roar install"},
	{Name: "install.ready_check", Kind: KindDuration, Default: "100ms", Slow: "500ms", Host: true, Doc: "Bootstrap only: how often install reads the screen for ROAR-READY"},
	{Name: "install.ready_timeout", Kind: KindDuration, Default: "20s", Slow: "2m", Host: true, Doc: "Bootstrap only"},

	{Name: "delivery.paste_check", Kind: KindDuration, Default: "100ms", Restart: true, Doc: "Inherited: paste proof checks"},
	{Name: "delivery.paste_wait", Kind: KindDuration, Default: "2s", Restart: true, Doc: "Inherited: how long the paste may take to show"},
	{Name: "delivery.submit_wait", Kind: KindDuration, Default: "5s", Restart: true, Doc: "Inherited: how long a submit may take to show"},
	{Name: "claude.poll", Kind: KindDuration, Default: "250ms", Restart: true, Doc: "Inherited: the Claude adapter's loop"},
	{Name: "claude.grace", Kind: KindDuration, Default: "2s", Restart: true, Doc: "Inherited: Claude adapter grace"},
	{Name: "claude.request_gap", Kind: KindDuration, Default: "30s", Restart: true, Doc: "Inherited: Claude adapter request gap"},
	{Name: "codex.deliver_timeout", Kind: KindDuration, Default: "15s", Restart: true, Doc: "Inherited: one Codex daemon call"},
	{Name: "agy.deliver_timeout", Kind: KindDuration, Default: "15s", Restart: true, Doc: "Inherited: one agentapi send-message call"},
}

var byName = func() map[string]Key {
	m := make(map[string]Key, len(keys))
	for _, k := range keys {
		m[k.Name] = k
	}
	return m
}()

// Keys lists every setting.
func Keys() []Key { return keys }

// Lookup finds one setting by name.
func Lookup(name string) (Key, bool) {
	k, ok := byName[name]
	return k, ok
}
