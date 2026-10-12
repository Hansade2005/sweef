//go:build darwin

package cocoa

import (
	"fmt"
	"structs"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// Geometry types as laid out by AppKit (CGFloat = float64 on 64-bit).
type NSPoint struct {
	_    structs.HostLayout
	X, Y float64
}

type NSSize struct {
	_             structs.HostLayout
	Width, Height float64
}

type NSRect struct {
	_      structs.HostLayout
	Origin NSPoint
	Size   NSSize
}

type NSRange struct {
	_                structs.HostLayout
	Location, Length uint
}

const nsNotFound = uint(0x7fffffffffffffff)

func rect(x, y, w, h float64) NSRect {
	return NSRect{Origin: NSPoint{X: x, Y: y}, Size: NSSize{Width: w, Height: h}}
}

// Core Graphics entry points (bound lazily in loadFrameworks).
var (
	cgColorSpaceCreateDeviceRGB  func() uintptr
	cgColorSpaceRelease          func(uintptr)
	cgDataProviderCreateWithData func(info, data, size, release uintptr) uintptr
	cgDataProviderRelease        func(uintptr)
	cgImageCreate                func(w, h, bpc, bpp, bpr, space uintptr, bitmapInfo uint32, provider, decode uintptr, interpolate bool, intent int32) uintptr
	cgImageRelease               func(uintptr)
	cgContextSaveGState          func(uintptr)
	cgContextRestoreGState       func(uintptr)
	cgContextTranslateCTM        func(ctx uintptr, tx, ty float64)
	cgContextScaleCTM            func(ctx uintptr, sx, sy float64)
	cgContextDrawImage           func(ctx uintptr, r NSRect, img uintptr)
	cgContextClipToRect          func(ctx uintptr, r NSRect)
	cgContextSetInterpolation    func(ctx uintptr, q int32)
	cgContextSetRGBFillColor     func(ctx uintptr, r, g, b, a float64)
	cgContextFillRect            func(ctx uintptr, r NSRect)

	libcCalloc func(n, size uintptr) uintptr
	libcFree   func(p uintptr)

	dispatchAsyncF func(queue, ctx, fn uintptr)
	mainQueue      uintptr
)

var loadOnce sync.Once
var loadErr error

func loadFrameworks() error {
	loadOnce.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				loadErr = fmt.Errorf("cocoa: loading frameworks: %v", r)
			}
		}()
		cocoaLib, err := purego.Dlopen("/System/Library/Frameworks/Cocoa.framework/Cocoa", purego.RTLD_GLOBAL|purego.RTLD_LAZY)
		if err != nil {
			loadErr = err
			return
		}
		_ = cocoaLib
		cg, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_GLOBAL|purego.RTLD_LAZY)
		if err != nil {
			loadErr = err
			return
		}
		sys, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_GLOBAL|purego.RTLD_LAZY)
		if err != nil {
			loadErr = err
			return
		}
		purego.RegisterLibFunc(&cgColorSpaceCreateDeviceRGB, cg, "CGColorSpaceCreateDeviceRGB")
		purego.RegisterLibFunc(&cgColorSpaceRelease, cg, "CGColorSpaceRelease")
		purego.RegisterLibFunc(&cgDataProviderCreateWithData, cg, "CGDataProviderCreateWithData")
		purego.RegisterLibFunc(&cgDataProviderRelease, cg, "CGDataProviderRelease")
		purego.RegisterLibFunc(&cgImageCreate, cg, "CGImageCreate")
		purego.RegisterLibFunc(&cgImageRelease, cg, "CGImageRelease")
		purego.RegisterLibFunc(&cgContextSaveGState, cg, "CGContextSaveGState")
		purego.RegisterLibFunc(&cgContextRestoreGState, cg, "CGContextRestoreGState")
		purego.RegisterLibFunc(&cgContextTranslateCTM, cg, "CGContextTranslateCTM")
		purego.RegisterLibFunc(&cgContextScaleCTM, cg, "CGContextScaleCTM")
		purego.RegisterLibFunc(&cgContextDrawImage, cg, "CGContextDrawImage")
		purego.RegisterLibFunc(&cgContextClipToRect, cg, "CGContextClipToRect")
		purego.RegisterLibFunc(&cgContextSetInterpolation, cg, "CGContextSetInterpolationQuality")
		purego.RegisterLibFunc(&cgContextSetRGBFillColor, cg, "CGContextSetRGBFillColor")
		purego.RegisterLibFunc(&cgContextFillRect, cg, "CGContextFillRect")
		purego.RegisterLibFunc(&libcCalloc, sys, "calloc")
		purego.RegisterLibFunc(&libcFree, sys, "free")
		purego.RegisterLibFunc(&dispatchAsyncF, sys, "dispatch_async_f")
		// dispatch_get_main_queue() is a macro for &_dispatch_main_q.
		mainQueue, err = purego.Dlsym(sys, "_dispatch_main_q")
		if err != nil {
			loadErr = err
		}
	})
	return loadErr
}

// ---- Objective-C helpers ---------------------------------------------------

var selCache sync.Map // string -> objc.SEL

// sel returns the (cached) selector for name.
func sel(name string) objc.SEL {
	if v, ok := selCache.Load(name); ok {
		return v.(objc.SEL)
	}
	s := objc.RegisterName(name)
	selCache.Store(name, s)
	return s
}

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

// send is shorthand for an id-returning message send.
func send(obj objc.ID, name string, args ...any) objc.ID { return obj.Send(sel(name), args...) }

func sendBool(obj objc.ID, name string, args ...any) bool {
	return objc.Send[bool](obj, sel(name), args...)
}

func sendFloat(obj objc.ID, name string, args ...any) float64 {
	return objc.Send[float64](obj, sel(name), args...)
}

func sendUint(obj objc.ID, name string, args ...any) uint {
	return objc.Send[uint](obj, sel(name), args...)
}

// nsString makes an autoreleased NSString from a Go string.
func nsString(s string) objc.ID {
	return send(class("NSString"), "stringWithUTF8String:", s)
}

// goString copies an NSString (or 0) into a Go string.
func goString(ns objc.ID) string {
	if ns == 0 {
		return ""
	}
	p := objc.Send[uintptr](ns, sel("UTF8String"))
	if p == 0 {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(ptrFrom(p), n)) != 0 {
		n++
		if n > 1<<26 {
			break
		}
	}
	return string(unsafe.Slice((*byte)(ptrFrom(p)), n))
}

func ptrFrom(u uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&u)) }

// autoreleasePool runs f inside an NSAutoreleasePool.
func autoreleasePool(f func()) {
	pool := send(send(class("NSAutoreleasePool"), "alloc"), "init")
	defer send(pool, "drain")
	f()
}
