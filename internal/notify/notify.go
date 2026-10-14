// Package notify delivers web notifications to the operating system and reports what the user did with them.
// The browser engine builds a Notification from the Notifications API; a Sink shows it. The native sinks are
// the freedesktop D-Bus service on Linux (internal/dbus, verified against a real dbus-daemon), the shell's balloon
// notification on Windows and NSUserNotification on macOS (those two are built and vetted but have never run
// on a real desktop). When no native service exists, Fallback hands the notification to the browser window,
// which shows an in-browser toast.
package notify

import (
	"errors"
	"sync"
)

// Action is a button on a notification (Notification.actions).
type Action struct {
	ID    string
	Title string
	Icon  string
}

// Notification is what is shown. Strings are plain text; sinks escape them for their platform.
type Notification struct {
	ID                 string // unique per notification, assigned by the browser
	Origin             string // "https://example.com": shown so the user knows who is speaking
	Title, Body        string
	IconPath           string // a local image file for the icon ("" = the browser's)
	Tag                string // a notification with the same origin and tag replaces the earlier one
	Renotify           bool
	Silent             bool
	RequireInteraction bool
	Timestamp          int64 // milliseconds since the epoch, 0 = now
	Actions            []Action
	Lang, Dir          string
}

// Kind of Event.
type Kind int

const (
	// Click: the user activated the notification body.
	Click Kind = iota + 1
	// ActionClick: the user pressed one of the action buttons (Event.Action is its id).
	ActionClick
	// Closed: the notification went away without a click (Event.Reason says why).
	Closed
	// Error: the notification could not be shown after Show returned.
	Error
)

// CloseReason says why a notification was closed.
type CloseReason int

const (
	ReasonUnknown     CloseReason = iota
	ReasonUser                    // the user dismissed it
	ReasonTimeout                 // the system removed it after its time
	ReasonApplication             // Sink.Close was called
)

// Event is something that happened to a shown notification.
type Event struct {
	Kind   Kind
	ID     string
	Action string
	Reason CloseReason
	Err    error
}

// Sink shows notifications.
type Sink interface {
	// Name is a short description for diagnostics ("freedesktop", "recorder", "toast").
	Name() string
	// Show displays n. It returns when the system accepted it; what the user does comes through the handler.
	Show(n Notification) error
	// Close removes a shown notification (no Closed event follows for it).
	Close(id string)
	// SetHandler registers the receiver of user events. It may be called from any goroutine and never blocks.
	SetHandler(func(Event))
	// Shutdown releases the sink's resources.
	Shutdown() error
}

// ErrUnavailable is returned by Native when the platform has no usable notification service.
var ErrUnavailable = errors.New("notify: no desktop notification service is available")

// handlerBox is the common storage of a sink's handler.
type handlerBox struct {
	mu sync.Mutex
	f  func(Event)
}

func (h *handlerBox) set(f func(Event)) {
	h.mu.Lock()
	h.f = f
	h.mu.Unlock()
}

func (h *handlerBox) emit(e Event) {
	h.mu.Lock()
	f := h.f
	h.mu.Unlock()
	if f != nil {
		f(e)
	}
}

// ---- Recorder: the sink tests use ------------------------------------------------------------------------------------

// Recorder is a Sink that remembers everything and lets a test play the user.
type Recorder struct {
	h       handlerBox
	mu      sync.Mutex
	shown   []Notification
	closed  []string
	failing error
	live    map[string]bool
}

// NewRecorder makes an empty recording sink.
func NewRecorder() *Recorder { return &Recorder{live: map[string]bool{}} }

func (r *Recorder) Name() string             { return "recorder" }
func (r *Recorder) SetHandler(f func(Event)) { r.h.set(f) }
func (r *Recorder) Shutdown() error          { return nil }
func (r *Recorder) FailNext(err error)       { r.mu.Lock(); r.failing = err; r.mu.Unlock() }
func (r *Recorder) Show(n Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failing != nil {
		err := r.failing
		r.failing = nil
		return err
	}
	r.shown = append(r.shown, n)
	r.live[n.ID] = true
	return nil
}

func (r *Recorder) Close(id string) {
	r.mu.Lock()
	r.closed = append(r.closed, id)
	delete(r.live, id)
	r.mu.Unlock()
}

// Shown returns every notification shown so far, oldest first.
func (r *Recorder) Shown() []Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Notification(nil), r.shown...)
}

// Closed returns the ids passed to Close.
func (r *Recorder) Closed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.closed...)
}

// Last returns the most recent notification.
func (r *Recorder) Last() (Notification, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.shown) == 0 {
		return Notification{}, false
	}
	return r.shown[len(r.shown)-1], true
}

// Click plays the user clicking a notification.
func (r *Recorder) Click(id string) { r.h.emit(Event{Kind: Click, ID: id}) }

// ClickAction plays the user pressing an action button.
func (r *Recorder) ClickAction(id, action string) {
	r.h.emit(Event{Kind: ActionClick, ID: id, Action: action})
}

// Dismiss plays the user closing a notification.
func (r *Recorder) Dismiss(id string) {
	r.mu.Lock()
	delete(r.live, id)
	r.mu.Unlock()
	r.h.emit(Event{Kind: Closed, ID: id, Reason: ReasonUser})
}

// ---- Fallback ---------------------------------------------------------------------------------------------------------

// ToastFunc shows a notification inside the browser window (the app's toast).
type ToastFunc func(Notification)

// Fallback tries the native sink first and falls back to an in-browser toast when there is none or it fails.
type Fallback struct {
	h       handlerBox
	Primary Sink // may be nil
	Toast   ToastFunc
	mu      sync.Mutex
	inToast map[string]bool
}

// NewFallback combines a (possibly nil) native sink with a toast function.
func NewFallback(primary Sink, toast ToastFunc) *Fallback {
	f := &Fallback{Primary: primary, Toast: toast, inToast: map[string]bool{}}
	if primary != nil {
		primary.SetHandler(f.h.emit)
	}
	return f
}

func (f *Fallback) Name() string {
	if f.Primary != nil {
		return f.Primary.Name() + "+toast"
	}
	return "toast"
}

func (f *Fallback) SetHandler(fn func(Event)) { f.h.set(fn) }

// Emit lets the window report what the user did with a toast.
func (f *Fallback) Emit(e Event) {
	f.mu.Lock()
	if e.Kind == Closed || e.Kind == Click {
		delete(f.inToast, e.ID)
	}
	f.mu.Unlock()
	f.h.emit(e)
}

func (f *Fallback) Show(n Notification) error {
	if f.Primary != nil {
		if err := f.Primary.Show(n); err == nil {
			return nil
		}
	}
	if f.Toast == nil {
		return ErrUnavailable
	}
	f.mu.Lock()
	f.inToast[n.ID] = true
	f.mu.Unlock()
	f.Toast(n)
	return nil
}

func (f *Fallback) Close(id string) {
	f.mu.Lock()
	t := f.inToast[id]
	delete(f.inToast, id)
	f.mu.Unlock()
	if !t && f.Primary != nil {
		f.Primary.Close(id)
	}
}

func (f *Fallback) Shutdown() error {
	if f.Primary != nil {
		return f.Primary.Shutdown()
	}
	return nil
}
