// Package sleepwatch bridges systemd-logind's PrepareForSleep signal
// onto a Go channel and holds a "delay" sleep inhibitor lock on the
// caller's behalf.
//
// Why this exists: when the laptop suspends with xray running, xray's
// outbound TCP sockets to the upstream server come back as stale
// TIME_WAIT zombies after resume — and tun2socks busy-loops on
// `connect: cannot assign requested address` until the watchdog
// catches it. The old fix was a /usr/lib/systemd/system-sleep hook
// kicking a user unit on resume; that hook never actually fired
// (oneshot systemd-suspend.service kills its cgroup, taking the
// hook's backgrounded subshell with it before it gets to call
// `systemctl --user start`). The current fix is what
// NetworkManager / systemd-networkd / wpa_supplicant do: subscribe
// to PrepareForSleep on the system bus, hold a delay inhibitor so
// logind waits while we shut xray down cleanly pre-suspend, and
// reconnect on resume — all in-process, no sudo bridges, no
// system-sleep shell scripts.
//
// The inhibitor lifecycle is owned by Watcher: it takes one on
// Connect, releases it via Event.Done() when the Pre event is
// handled (so logind can actually go to sleep), and re-takes it
// before emitting the matching Post event so the next sleep cycle
// is already armed.
package sleepwatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	logindService   = "org.freedesktop.login1"
	logindPath      = "/org/freedesktop/login1"
	logindInterface = "org.freedesktop.login1.Manager"
	signalName      = "PrepareForSleep"
)

// Event is what a consumer receives on Watcher.Events().
//
// Pre==true means logind is about to suspend the system and is
// currently holding off because of our delay inhibitor. The consumer
// has at most InhibitDelayMaxSec (logind default: 5s) to do cleanup
// and call Done() to release the inhibitor — after that logind
// suspends anyway.
//
// Pre==false means the system just resumed. Done is a no-op (the
// inhibitor for the next cycle has already been re-taken before
// this event was emitted) but is still provided so consumers can
// treat both events uniformly.
type Event struct {
	Pre  bool
	Done func()
}

// Watcher owns one D-Bus connection, one inhibitor fd, and one
// signal subscription. It is safe to Close concurrently with
// Events() consumption.
type Watcher struct {
	conn   *dbus.Conn
	events chan Event
	who    string
	why    string

	// cancel stops the internal signal-dispatch goroutine. Set by
	// Connect, called by Close.
	cancel context.CancelFunc

	mu      sync.Mutex
	inhibit *os.File // delay inhibitor fd, nil while suspended or after Close
}

// Connect opens the system bus, subscribes to PrepareForSleep and
// takes the initial delay inhibitor. The returned Watcher emits
// events on Events() until ctx is cancelled or Close is called.
//
// who is the human-readable inhibitor owner shown by `loginctl
// list-inhibitors`; why is the reason. logind requires both.
func Connect(ctx context.Context, who, why string) (*Watcher, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("sleepwatch: connect system bus: %w", err)
	}
	if !conn.SupportsUnixFDs() {
		_ = conn.Close()
		return nil, errors.New("sleepwatch: system bus does not support unix FD passing (required for inhibitor lock)")
	}

	loopCtx, cancel := context.WithCancel(ctx)
	w := &Watcher{
		conn:   conn,
		events: make(chan Event, 2),
		who:    who,
		why:    why,
		cancel: cancel,
	}

	if err := w.takeInhibitor(); err != nil {
		_ = conn.Close()
		cancel()
		return nil, err
	}

	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(logindPath),
		dbus.WithMatchInterface(logindInterface),
		dbus.WithMatchMember(signalName),
	); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("sleepwatch: AddMatchSignal: %w", err)
	}

	// Buffered generously. godbus's defaultSignalHandler.deliver() does
	// a non-blocking send first; on overflow it spawns a goroutine per
	// signal (`deferredDeliver`) that blocks on this channel send and
	// retains a *dbus.Signal until drained. Under a system-bus signal
	// burst (e.g. NetworkManager state churn, systemd unit activity) a
	// small buffer means hundreds of those goroutines pile up, each
	// pinning a Signal struct — that is how a "should-be-idle" watchdog
	// process ends up sitting on hundreds of MB of heap. We always
	// drain in the loop below in O(1) per signal so the larger buffer
	// just absorbs bursts without ever needing the goroutine fallback.
	sigCh := make(chan *dbus.Signal, 1024)
	conn.Signal(sigCh)
	go w.loop(loopCtx, sigCh)
	return w, nil
}

// Events returns the receive end of the event channel. Buffered (size
// 2) so a Pre event delivered while the consumer is mid-tick doesn't
// stall the dispatcher; if both Pre and Post arrive faster than the
// consumer drains, the channel fills and the dispatcher drops further
// signals — which is safe because the inhibitor will then keep
// suspend gated until the consumer catches up.
func (w *Watcher) Events() <-chan Event {
	return w.events
}

// Close releases the inhibitor (if held) and tears down the D-Bus
// connection. Idempotent — safe to call from a defer even if
// Connect failed mid-way.
func (w *Watcher) Close() error {
	w.cancel()
	w.releaseInhibitor()
	return w.conn.Close()
}

// takeInhibitor calls Manager.Inhibit("sleep", who, why, "delay") and
// stores the returned file descriptor. logind keeps "delay"
// inhibitors waiting for at most InhibitDelayMaxSec — long enough
// for us to stop xray cleanly, short enough that a buggy consumer
// can never indefinitely block sleep.
func (w *Watcher) takeInhibitor() error {
	obj := w.conn.Object(logindService, dbus.ObjectPath(logindPath))
	var fd dbus.UnixFD
	if err := obj.Call(logindInterface+".Inhibit", 0,
		"sleep", w.who, w.why, "delay",
	).Store(&fd); err != nil {
		return fmt.Errorf("sleepwatch: Inhibit: %w", err)
	}
	w.mu.Lock()
	w.inhibit = os.NewFile(uintptr(fd), "logind-inhibit")
	w.mu.Unlock()
	return nil
}

// releaseInhibitor closes the held inhibitor fd, if any. After this
// returns logind will let any pending suspend proceed (assuming no
// other delay inhibitors are held).
func (w *Watcher) releaseInhibitor() {
	w.mu.Lock()
	fd := w.inhibit
	w.inhibit = nil
	w.mu.Unlock()
	if fd != nil {
		_ = fd.Close()
	}
}

// loop fans D-Bus signals into Event values. Re-taking the inhibitor
// on Post happens here, before the event is emitted, so a consumer
// that handles Post quickly is immediately armed for the next cycle
// without having to know the inhibitor protocol.
func (w *Watcher) loop(ctx context.Context, sigCh <-chan *dbus.Signal) {
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-sigCh:
			if !ok {
				return
			}
			if sig == nil || sig.Name != logindInterface+"."+signalName {
				continue
			}
			if len(sig.Body) < 1 {
				continue
			}
			pre, ok := sig.Body[0].(bool)
			if !ok {
				continue
			}
			ev := Event{Pre: pre, Done: func() {}}
			if pre {
				// Hand the consumer the release callback so it can
				// run cleanup first, then signal logind it's safe to
				// proceed by calling Done.
				ev.Done = w.releaseInhibitor
			} else {
				// Re-arm before notifying. A failure here is
				// recoverable: post-resume reconnect still happens,
				// only the *next* suspend cycle would skip the
				// pre-suspend phase. Log to stderr so the user has a
				// breadcrumb if it keeps repeating.
				if err := w.takeInhibitor(); err != nil {
					fmt.Fprintf(os.Stderr, "sleepwatch: re-arm inhibitor after resume: %v\n", err)
				}
			}
			select {
			case w.events <- ev:
			case <-ctx.Done():
				return
			default:
				// Channel full — consumer is wedged. Drop the event
				// rather than block the dispatcher. For Pre this is
				// arguably bad (we'd want to still release the
				// inhibitor so the machine can sleep) but a wedged
				// consumer is a bug anyway and the inhibitor max
				// timeout will catch it.
				fmt.Fprintln(os.Stderr, "sleepwatch: event channel full, dropping signal")
			}
		}
	}
}
