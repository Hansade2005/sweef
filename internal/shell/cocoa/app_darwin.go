//go:build darwin

package cocoa

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/sweefbrowser/sweef/internal/shell"
	"github.com/sweefbrowser/sweef/internal/shell/mainthread"
)

// platform implements mainthread.Platform with the real AppKit run loop:
// Run is [NSApp run]; Post goes through libdispatch's main queue, whose work
// items the NSApplication run loop executes on the main thread.
type platform struct {
	mu      sync.Mutex
	nextID  uintptr
	pending map[uintptr]func()
	stopped atomic.Bool
}

var (
	plat       = &platform{pending: map[uintptr]func(){}}
	nsApp      objc.ID
	appDelOnce sync.Once
	trampoline uintptr
)

func init() {
	// Make mainthread.Run drive AppKit instead of its plain channel loop.
	mainthread.SetPlatform(plat)
}

// guard keeps a Go panic from unwinding through AppKit's C frames.
func guard(name string) {
	if r := recover(); r != nil {
		fmt.Fprintf(os.Stderr, "cocoa: panic in %s: %v\n", name, r)
	}
}

func (p *platform) Run() {
	if err := loadFrameworks(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	autoreleasePool(func() {
		nsApp = send(class("NSApplication"), "sharedApplication")
		send(nsApp, "setActivationPolicy:", int(0)) // NSApplicationActivationPolicyRegular
		installAppDelegate()
		installMenu()
	})
	tr := purego.NewCallback(func(ctx uintptr) {
		defer guard("dispatch")
		p.mu.Lock()
		f := p.pending[ctx]
		delete(p.pending, ctx)
		p.mu.Unlock()
		if f != nil {
			autoreleasePool(f)
		}
	})
	p.mu.Lock()
	trampoline = tr
	p.mu.Unlock()
	p.drainPendingBeforeRun()
	send(nsApp, "activateIgnoringOtherApps:", true)
	send(nsApp, "run")
}

// drainPendingBeforeRun re-dispatches anything posted before Run started.
func (p *platform) drainPendingBeforeRun() {
	p.mu.Lock()
	ids := make([]uintptr, 0, len(p.pending))
	for id := range p.pending {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	for _, id := range ids {
		dispatchAsyncF(mainQueue, id, trampoline)
	}
}

func (p *platform) Post(f func()) {
	if p.stopped.Load() || loadFrameworks() != nil {
		return
	}
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	p.pending[id] = f
	tr := trampoline
	p.mu.Unlock()
	if tr == 0 {
		return // Run has not created the trampoline yet; drainPendingBeforeRun sends it
	}
	dispatchAsyncF(mainQueue, id, tr)
}

// Stop ends [NSApp run]. It is called from the app goroutine after the app
// function returned.
func (p *platform) Stop() {
	if !p.stopped.CompareAndSwap(false, true) || nsApp == 0 {
		return
	}
	stop := func() {
		send(nsApp, "stop:", objc.ID(0))
		// stop: only takes effect after the next event; post a dummy one.
		ev := send(class("NSEvent"), "otherEventWithType:location:modifierFlags:timestamp:windowNumber:context:subtype:data1:data2:",
			uint(15), NSPoint{}, uint(0), float64(0), int(0), objc.ID(0), int16(0), int(0), int(0))
		send(nsApp, "postEvent:atStart:", ev, true)
	}
	// bypass Post's stopped check
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	p.pending[id] = stop
	tr := trampoline
	p.mu.Unlock()
	if tr != 0 {
		dispatchAsyncF(mainQueue, id, tr)
	}
}

// ---- app delegate and menu ---------------------------------------------------

var appDelegate objc.ID

func installAppDelegate() {
	appDelOnce.Do(func() {
		cls, err := objc.RegisterClass("SweefAppDelegate", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
			{Cmd: sel("applicationShouldTerminate:"), Fn: func(self objc.ID, _ objc.SEL, sender objc.ID) uint {
				defer guard("applicationShouldTerminate")
				// Route Cmd+Q / Dock Quit to the app as a close request.
				windowsMu.Lock()
				for _, w := range windowsByWin {
					w.q.Push(shell.Event{Kind: shell.EvClose})
				}
				windowsMu.Unlock()
				return 0 // NSTerminateCancel: the app decides when to exit
			}},
			{Cmd: sel("applicationShouldTerminateAfterLastWindowClosed:"), Fn: func(self objc.ID, _ objc.SEL, sender objc.ID) bool {
				return false
			}},
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "cocoa: app delegate:", err)
			return
		}
		appDelegate = send(send(objc.ID(cls), "alloc"), "init")
		send(nsApp, "setDelegate:", appDelegate)
	})
}

func menuItem(title, action, key string) objc.ID {
	var act objc.SEL
	if action != "" {
		act = sel(action)
	}
	it := send(send(class("NSMenuItem"), "alloc"), "initWithTitle:action:keyEquivalent:", nsString(title), act, nsString(key))
	return it
}

func installMenu() {
	bar := send(send(class("NSMenu"), "alloc"), "init")
	appItem := send(send(class("NSMenuItem"), "alloc"), "init")
	send(bar, "addItem:", appItem)
	appMenu := send(send(class("NSMenu"), "alloc"), "init")
	send(appMenu, "addItem:", menuItem("Hide Sweef", "hide:", "h"))
	send(appMenu, "addItem:", menuItem("Minimize", "performMiniaturize:", "m"))
	send(appMenu, "addItem:", send(class("NSMenuItem"), "separatorItem"))
	send(appMenu, "addItem:", menuItem("Quit Sweef", "terminate:", "q"))
	send(appItem, "setSubmenu:", appMenu)
	send(nsApp, "setMainMenu:", bar)
}
