//go:build darwin

package notify

// macOS notifications through NSUserNotificationCenter (Foundation), called with purego's Objective-C runtime
// bindings, so no cgo is involved. A delegate class is registered at run time to receive activations.
//
// UNVERIFIED: compiled and vetted for darwin/arm64 and darwin/amd64 but never run on a Mac. NSUserNotification is
// deprecated in favour of UNUserNotificationCenter, whose API needs completion blocks and a signed application
// bundle; the older API is used because it can be driven without blocks. It requires the process to have a bundle
// identifier (the packaged Sweef.app has one); from a bare binary the center is nil and Native reports
// ErrUnavailable so the browser shows its in-window toast instead.

import (
	"errors"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

type macSink struct {
	h      handlerBox
	center objc.ID
	mu     sync.Mutex
	live   map[string]objc.ID // our id -> NSUserNotification
}

var theMac *macSink

// Native opens the macOS notification center.
func Native(appName string) (Sink, error) {
	if theMac != nil {
		return nil, errors.New("notify: the macOS sink is already open")
	}
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = ErrUnavailable
			}
		}()
		_, err = purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_GLOBAL|purego.RTLD_LAZY)
	}()
	if err != nil {
		return nil, errors.Join(ErrUnavailable, err)
	}
	cls := objc.GetClass("NSUserNotificationCenter")
	if cls == 0 {
		return nil, ErrUnavailable
	}
	center := objc.ID(cls).Send(objc.RegisterName("defaultUserNotificationCenter"))
	if center == 0 {
		return nil, ErrUnavailable
	}
	s := &macSink{center: center, live: map[string]objc.ID{}}
	theMac = s
	delegateClass, err := objc.RegisterClass("SweefNotifyDelegate", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: objc.RegisterName("userNotificationCenter:didActivateNotification:"), Fn: func(self objc.ID, _cmd objc.SEL, c, note objc.ID) {
			theMac.activated(note)
		}},
		{Cmd: objc.RegisterName("userNotificationCenter:shouldPresentNotification:"), Fn: func(self objc.ID, _cmd objc.SEL, c, note objc.ID) bool {
			return true // show it even while Sweef is the active application
		}},
	})
	if err != nil {
		theMac = nil
		return nil, errors.Join(ErrUnavailable, err)
	}
	delegate := objc.ID(delegateClass).Send(objc.RegisterName("new"))
	center.Send(objc.RegisterName("setDelegate:"), delegate)
	return s, nil
}

func nsstr(s string) objc.ID {
	return objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), s)
}

func goStr(ns objc.ID) string {
	if ns == 0 {
		return ""
	}
	p := objc.Send[uintptr](ns, objc.RegisterName("UTF8String"))
	if p == 0 {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(*(*unsafe.Pointer)(unsafe.Pointer(&p)), n)) != 0 && n < 1<<20 {
		n++
	}
	return string(unsafe.Slice((*byte)(*(*unsafe.Pointer)(unsafe.Pointer(&p))), n))
}

func (s *macSink) Name() string             { return "macos-nsusernotification" }
func (s *macSink) SetHandler(f func(Event)) { s.h.set(f) }

func (s *macSink) activated(note objc.ID) {
	id := goStr(note.Send(objc.RegisterName("identifier")))
	s.mu.Lock()
	_, ok := s.live[id]
	delete(s.live, id)
	s.mu.Unlock()
	if ok {
		s.h.emit(Event{Kind: Click, ID: id})
	}
}

func (s *macSink) Show(n Notification) error {
	note := objc.ID(objc.GetClass("NSUserNotification")).Send(objc.RegisterName("new"))
	if note == 0 {
		return ErrUnavailable
	}
	title := n.Title
	note.Send(objc.RegisterName("setTitle:"), nsstr(title))
	if n.Origin != "" {
		note.Send(objc.RegisterName("setSubtitle:"), nsstr(n.Origin))
	}
	note.Send(objc.RegisterName("setInformativeText:"), nsstr(n.Body))
	note.Send(objc.RegisterName("setIdentifier:"), nsstr(n.ID))
	if !n.Silent {
		note.Send(objc.RegisterName("setSoundName:"), nsstr("NSUserNotificationDefaultSoundName"))
	}
	s.mu.Lock()
	s.live[n.ID] = note
	s.mu.Unlock()
	s.center.Send(objc.RegisterName("deliverNotification:"), note)
	return nil
}

func (s *macSink) Close(id string) {
	s.mu.Lock()
	note := s.live[id]
	delete(s.live, id)
	s.mu.Unlock()
	if note != 0 {
		s.center.Send(objc.RegisterName("removeDeliveredNotification:"), note)
	}
}

func (s *macSink) Shutdown() error {
	s.center.Send(objc.RegisterName("removeAllDeliveredNotifications"))
	theMac = nil
	return nil
}
