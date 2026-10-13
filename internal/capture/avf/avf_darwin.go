//go:build darwin

// UNVERIFIED: never run on real hardware. This file is compiled and vetted for darwin/amd64 and darwin/arm64 only;
// nobody has run it on a Mac. docs/capture.md lists what to check.

// Package avf is the macOS capture backend: AVFoundation cameras and microphones through purego's Objective-C
// runtime (no cgo). A capture session delivers sample buffers to a delegate class registered at run time; the
// delegate converts them (NV12 video, 16-bit PCM audio) and pushes them to the channels of the capture package.
//
// macOS only lets a process use the camera or microphone when the app bundle carries NSCameraUsageDescription and
// NSMicrophoneUsageDescription (packaging/macos/Info.plist.in) and, with the hardened runtime, the matching
// entitlements (packaging/macos/entitlements.plist). Without the usage string the system kills the process on the
// first access, so this package checks for it first and fails with an error instead.
package avf

import (
	"errors"
	"fmt"
	"structs"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/sweefbrowser/sweef/internal/capture"
)

var (
	loadOnce sync.Once
	loadErr  error

	cmSampleBufferGetImageBuffer           func(sb uintptr) uintptr
	cmSampleBufferGetFormatDescription     func(sb uintptr) uintptr
	cmSampleBufferGetDataBuffer            func(sb uintptr) uintptr
	cmAudioFormatDescriptionGetStreamBasic func(fd uintptr) uintptr
	cmBlockBufferGetDataLength             func(bb uintptr) uintptr
	cmBlockBufferCopyDataBytes             func(bb, offset, length, dst uintptr) int32
	cvPixelBufferLockBaseAddress           func(pb uintptr, flags uint64) int32
	cvPixelBufferUnlockBaseAddress         func(pb uintptr, flags uint64) int32
	cvPixelBufferGetWidth                  func(pb uintptr) uintptr
	cvPixelBufferGetHeight                 func(pb uintptr) uintptr
	cvPixelBufferGetBaseAddressOfPlane     func(pb uintptr, plane uintptr) uintptr
	cvPixelBufferGetBytesPerRowOfPlane     func(pb uintptr, plane uintptr) uintptr
	dispatchQueueCreate                    func(label *byte, attr uintptr) uintptr
	dispatchRelease                        func(obj uintptr)

	videoSinkClass, audioSinkClass objc.Class
)

// audioStreamBasicDescription is CoreAudio's AudioStreamBasicDescription.
type audioStreamBasicDescription struct {
	_                structs.HostLayout
	SampleRate       float64
	FormatID         uint32
	FormatFlags      uint32
	BytesPerPacket   uint32
	FramesPerPacket  uint32
	BytesPerFrame    uint32
	ChannelsPerFrame uint32
	BitsPerChannel   uint32
	Reserved         uint32
}

const (
	pixelFormat420v                                               = 0x34323076 // kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange: NV12 with limited range
	formatLPCM                                                    = 0x6C70636D // kAudioFormatLinearPCM
	authNotDetermined, authDenied, authRestricted, authAuthorized = 0, 2, 1, 3
)

func sel(n string) objc.SEL                      { return objc.RegisterName(n) }
func class(n string) objc.ID                     { return objc.ID(objc.GetClass(n)) }
func send(o objc.ID, n string, a ...any) objc.ID { return o.Send(sel(n), a...) }
func ptr(u uintptr) unsafe.Pointer               { return *(*unsafe.Pointer)(unsafe.Pointer(&u)) }
func nsString(s string) objc.ID                  { return send(class("NSString"), "stringWithUTF8String:", s) }
func nsNumber(v uint32) objc.ID                  { return send(class("NSNumber"), "numberWithUnsignedInt:", v) }

func goString(ns objc.ID) string {
	if ns == 0 {
		return ""
	}
	p := objc.Send[uintptr](ns, sel("UTF8String"))
	if p == 0 {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(ptr(p), n)) != 0 && n < 1<<20 {
		n++
	}
	return string(unsafe.Slice((*byte)(ptr(p)), n))
}

func load() error {
	loadOnce.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				loadErr = fmt.Errorf("avf: loading frameworks: %v", r)
			}
		}()
		open := func(p string) uintptr {
			h, err := purego.Dlopen(p, purego.RTLD_GLOBAL|purego.RTLD_LAZY)
			if err != nil && loadErr == nil {
				loadErr = err
			}
			return h
		}
		open("/System/Library/Frameworks/AVFoundation.framework/AVFoundation")
		cm := open("/System/Library/Frameworks/CoreMedia.framework/CoreMedia")
		cv := open("/System/Library/Frameworks/CoreVideo.framework/CoreVideo")
		sys := open("/usr/lib/libSystem.B.dylib")
		if loadErr != nil {
			return
		}
		purego.RegisterLibFunc(&cmSampleBufferGetImageBuffer, cm, "CMSampleBufferGetImageBuffer")
		purego.RegisterLibFunc(&cmSampleBufferGetFormatDescription, cm, "CMSampleBufferGetFormatDescription")
		purego.RegisterLibFunc(&cmSampleBufferGetDataBuffer, cm, "CMSampleBufferGetDataBuffer")
		purego.RegisterLibFunc(&cmAudioFormatDescriptionGetStreamBasic, cm, "CMAudioFormatDescriptionGetStreamBasicDescription")
		purego.RegisterLibFunc(&cmBlockBufferGetDataLength, cm, "CMBlockBufferGetDataLength")
		purego.RegisterLibFunc(&cmBlockBufferCopyDataBytes, cm, "CMBlockBufferCopyDataBytes")
		purego.RegisterLibFunc(&cvPixelBufferLockBaseAddress, cv, "CVPixelBufferLockBaseAddress")
		purego.RegisterLibFunc(&cvPixelBufferUnlockBaseAddress, cv, "CVPixelBufferUnlockBaseAddress")
		purego.RegisterLibFunc(&cvPixelBufferGetWidth, cv, "CVPixelBufferGetWidth")
		purego.RegisterLibFunc(&cvPixelBufferGetHeight, cv, "CVPixelBufferGetHeight")
		purego.RegisterLibFunc(&cvPixelBufferGetBaseAddressOfPlane, cv, "CVPixelBufferGetBaseAddressOfPlane")
		purego.RegisterLibFunc(&cvPixelBufferGetBytesPerRowOfPlane, cv, "CVPixelBufferGetBytesPerRowOfPlane")
		purego.RegisterLibFunc(&dispatchQueueCreate, sys, "dispatch_queue_create")
		purego.RegisterLibFunc(&dispatchRelease, sys, "dispatch_release")

		// The delegates: one class per media type, found again from the receiver in the callback.
		mk := func(name string, fn any) objc.Class {
			c, err := objc.RegisterClass(name, objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
				{Cmd: sel("captureOutput:didOutputSampleBuffer:fromConnection:"), Fn: fn},
			})
			if err != nil && loadErr == nil {
				loadErr = err
			}
			return c
		}
		videoSinkClass = mk("SweefVideoSink", func(self objc.ID, _ objc.SEL, _ objc.ID, sb uintptr, _ objc.ID) {
			defer func() { _ = recover() }() // a panic must not unwind into Objective-C
			if s, ok := sinks.Load(self); ok {
				s.(*session).onVideo(sb)
			}
		})
		audioSinkClass = mk("SweefAudioSink", func(self objc.ID, _ objc.SEL, _ objc.ID, sb uintptr, _ objc.ID) {
			defer func() { _ = recover() }()
			if s, ok := sinks.Load(self); ok {
				s.(*session).onAudio(sb)
			}
		})
	})
	return loadErr
}

var sinks sync.Map // delegate objc.ID -> *session

// access checks (and if needed asks for) permission to use a media type ("vide" or "soun").
func access(media, plistKey string) error {
	cls := class("AVCaptureDevice")
	st := objc.Send[int](cls, sel("authorizationStatusForMediaType:"), nsString(media))
	switch st {
	case authAuthorized:
		return nil
	case authDenied, authRestricted:
		return errors.New("avf: access denied by macOS (System Settings > Privacy & Security > Camera / Microphone)")
	}
	// Not determined: asking without the usage string in the bundle makes macOS kill the process.
	if send(send(class("NSBundle"), "mainBundle"), "objectForInfoDictionaryKey:", nsString(plistKey)) == 0 {
		return fmt.Errorf("avf: this build is not an app bundle with %s; run the packaged Sweef.app to use the camera or microphone", plistKey)
	}
	done := make(chan bool, 1)
	blk := objc.NewBlock(func(_ objc.Block, granted bool) { done <- granted })
	defer blk.Release()
	send(cls, "requestAccessForMediaType:completionHandler:", nsString(media), blk)
	select {
	case ok := <-done:
		if ok {
			return nil
		}
		return errors.New("avf: access denied by the user")
	case <-time.After(2 * time.Minute):
		return errors.New("avf: the system permission prompt was not answered")
	}
}

// ---- devices ------------------------------------------------------------------------------------------------

type devInfo struct{ id, name string }

func devices(media string) []devInfo {
	if load() != nil {
		return nil
	}
	var out []devInfo
	func() {
		defer func() { _ = recover() }()
		pool := send(send(class("NSAutoreleasePool"), "alloc"), "init")
		defer send(pool, "drain")
		arr := send(class("AVCaptureDevice"), "devicesWithMediaType:", nsString(media))
		n := objc.Send[uint](arr, sel("count"))
		for i := uint(0); i < n; i++ {
			d := send(arr, "objectAtIndex:", i)
			out = append(out, devInfo{id: goString(send(d, "uniqueID")), name: goString(send(d, "localizedName"))})
		}
	}()
	return out
}

// ---- a running capture session ----------------------------------------------------------------------------------

type session struct {
	video  bool
	vout   chan *capture.Frame
	aout   chan *capture.AudioChunk
	w, h   int
	rate   int
	ch     int
	ready  chan struct{}
	once   sync.Once
	start  time.Time
	nAudio int
	mu     sync.Mutex
	closed bool

	sess, sink, queue, output objc.ID
	queueRef                  uintptr
}

func (s *session) markReady() { s.once.Do(func() { close(s.ready) }) }

func (s *session) onVideo(sb uintptr) {
	pb := cmSampleBufferGetImageBuffer(sb)
	if pb == 0 || cvPixelBufferLockBaseAddress(pb, 1) != 0 { // 1 = kCVPixelBufferLock_ReadOnly
		return
	}
	defer cvPixelBufferUnlockBaseAddress(pb, 1)
	w, h := int(cvPixelBufferGetWidth(pb)), int(cvPixelBufferGetHeight(pb))
	yp, yStride := cvPixelBufferGetBaseAddressOfPlane(pb, 0), int(cvPixelBufferGetBytesPerRowOfPlane(pb, 0))
	up, uStride := cvPixelBufferGetBaseAddressOfPlane(pb, 1), int(cvPixelBufferGetBytesPerRowOfPlane(pb, 1))
	if yp == 0 || up == 0 || w <= 0 || h <= 0 {
		return
	}
	// the two planes need not be adjacent: lay them out as one NV12 buffer for capture.Convert
	stride := yStride
	buf := make([]byte, stride*h+stride*((h+1)/2))
	for r := 0; r < h; r++ {
		copy(buf[r*stride:r*stride+w], unsafe.Slice((*byte)(ptr(yp+uintptr(r*yStride))), w))
	}
	for r := 0; r < (h+1)/2; r++ {
		n := min(uStride, stride)
		copy(buf[stride*h+r*stride:stride*h+r*stride+n], unsafe.Slice((*byte)(ptr(up+uintptr(r*uStride))), n))
	}
	f := capture.NewFrame(w, h)
	if capture.Convert(f, capture.PixNV12, buf, stride) != nil {
		return
	}
	f.PTS = time.Since(s.start)
	s.mu.Lock()
	s.w, s.h = w, h
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	push(s.vout, f)
	s.markReady()
}

func (s *session) onAudio(sb uintptr) {
	bb := cmSampleBufferGetDataBuffer(sb)
	fd := cmSampleBufferGetFormatDescription(sb)
	if bb == 0 || fd == 0 {
		return
	}
	asbd := (*audioStreamBasicDescription)(ptr(cmAudioFormatDescriptionGetStreamBasic(fd)))
	if asbd == nil || asbd.FormatID != formatLPCM || asbd.BitsPerChannel != 16 {
		return
	}
	n := int(cmBlockBufferGetDataLength(bb))
	raw := make([]byte, n)
	if n == 0 || cmBlockBufferCopyDataBytes(bb, 0, uintptr(n), uintptr(unsafe.Pointer(&raw[0]))) != 0 {
		return
	}
	pcm := make([]int16, n/2)
	for i := range pcm {
		pcm[i] = int16(uint16(raw[2*i]) | uint16(raw[2*i+1])<<8)
	}
	s.mu.Lock()
	s.rate, s.ch = int(asbd.SampleRate), int(asbd.ChannelsPerFrame)
	closed := s.closed
	s.mu.Unlock()
	if closed || s.ch == 0 {
		return
	}
	a := &capture.AudioChunk{Rate: s.rate, Channels: s.ch, PCM: pcm, PTS: time.Since(s.start)}
	pushAudio(s.aout, a)
	s.markReady()
}

func push(c chan *capture.Frame, f *capture.Frame) {
	select {
	case c <- f:
	default:
		select {
		case <-c:
		default:
		}
		select {
		case c <- f:
		default:
		}
	}
}

func pushAudio(c chan *capture.AudioChunk, a *capture.AudioChunk) {
	select {
	case c <- a:
	default:
		select {
		case <-c:
		default:
		}
		select {
		case c <- a:
		default:
		}
	}
}

// presetFor picks the AVCaptureSession preset nearest to a requested size.
func presetFor(w, h int) string {
	switch {
	case w >= 1600 || h >= 900:
		return "AVCaptureSessionPreset1920x1080"
	case w >= 1000 || h >= 600:
		return "AVCaptureSessionPreset1280x720"
	case w >= 500 || h >= 400:
		return "AVCaptureSessionPreset640x480"
	}
	return "AVCaptureSessionPreset352x288"
}

// startSession builds and starts a capture session for device id.
func startSession(id, media string, vc capture.VideoConstraints, chans int) (*session, error) {
	if err := load(); err != nil {
		return nil, err
	}
	key := "NSCameraUsageDescription"
	if media == "soun" {
		key = "NSMicrophoneUsageDescription"
	}
	if err := access(media, key); err != nil {
		return nil, err
	}
	s := &session{video: media == "vide", ready: make(chan struct{}), start: time.Now(), vout: make(chan *capture.Frame, 3), aout: make(chan *capture.AudioChunk, 16)}
	var startErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				startErr = fmt.Errorf("avf: %v", r)
			}
		}()
		dev := send(class("AVCaptureDevice"), "deviceWithUniqueID:", nsString(id))
		if dev == 0 {
			startErr = capture.ErrNotFound
			return
		}
		input := send(class("AVCaptureDeviceInput"), "deviceInputWithDevice:error:", dev, uintptr(0))
		if input == 0 {
			startErr = fmt.Errorf("%w: the device could not be opened (another application may hold it)", capture.ErrInUse)
			return
		}
		s.sess = send(send(class("AVCaptureSession"), "alloc"), "init")
		if !objc.Send[bool](s.sess, sel("canAddInput:"), input) {
			startErr = fmt.Errorf("%w: the capture session refused the device", capture.ErrInUse)
			return
		}
		send(s.sess, "addInput:", input)
		if s.video {
			preset := nsString(presetFor(vc.Width, vc.Height))
			if objc.Send[bool](s.sess, sel("canSetSessionPreset:"), preset) {
				send(s.sess, "setSessionPreset:", preset)
			}
			s.output = send(send(class("AVCaptureVideoDataOutput"), "alloc"), "init")
			settings := send(class("NSDictionary"), "dictionaryWithObject:forKey:", nsNumber(pixelFormat420v), nsString("PixelFormatType"))
			send(s.output, "setVideoSettings:", settings)
			send(s.output, "setAlwaysDiscardsLateVideoFrames:", true)
			s.sink = send(send(objc.ID(videoSinkClass), "alloc"), "init")
		} else {
			s.output = send(send(class("AVCaptureAudioDataOutput"), "alloc"), "init")
			ks := []string{"AVFormatIDKey", "AVLinearPCMBitDepthKey", "AVLinearPCMIsFloatKey", "AVLinearPCMIsBigEndianKey", "AVLinearPCMIsNonInterleaved", "AVNumberOfChannelsKey"}
			vs := []objc.ID{nsNumber(formatLPCM), nsNumber(16), nsNumber(0), nsNumber(0), nsNumber(0), nsNumber(uint32(max(chans, 1)))}
			keys, vals := send(class("NSMutableArray"), "array"), send(class("NSMutableArray"), "array")
			for i := range ks {
				send(keys, "addObject:", nsString(ks[i]))
				send(vals, "addObject:", vs[i])
			}
			send(s.output, "setAudioSettings:", send(class("NSDictionary"), "dictionaryWithObjects:forKeys:", vals, keys))
			s.sink = send(send(objc.ID(audioSinkClass), "alloc"), "init")
		}
		sinks.Store(s.sink, s)
		label := append([]byte("org.sweef.capture."+media), 0)
		s.queueRef = dispatchQueueCreate(&label[0], 0)
		send(s.output, "setSampleBufferDelegate:queue:", s.sink, s.queueRef)
		if !objc.Send[bool](s.sess, sel("canAddOutput:"), s.output) {
			startErr = errors.New("avf: the capture session refused the output")
			return
		}
		send(s.sess, "addOutput:", s.output)
		send(s.sess, "startRunning")
	}()
	if startErr != nil {
		s.stop()
		return nil, startErr
	}
	select {
	case <-s.ready:
		return s, nil
	case <-time.After(5 * time.Second):
		s.stop()
		return nil, capture.ErrTimeout
	}
}

func (s *session) stop() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	defer func() { _ = recover() }()
	if s.sess != 0 {
		send(s.sess, "stopRunning")
	}
	if s.output != 0 {
		send(s.output, "setSampleBufferDelegate:queue:", objc.ID(0), uintptr(0))
	}
	if s.sink != 0 {
		sinks.Delete(s.sink)
		send(s.sink, "release")
	}
	if s.output != 0 {
		send(s.output, "release")
	}
	if s.sess != 0 {
		send(s.sess, "release")
	}
	if s.queueRef != 0 {
		dispatchRelease(s.queueRef)
	}
}

// ---- capture.Provider -----------------------------------------------------------------------------------------

// Provider lists AVFoundation cameras and microphones.
type Provider struct {
	mu   sync.Mutex
	at   time.Time
	cams []devInfo
	mics []devInfo
}

// NewProvider returns the macOS provider.
func NewProvider() *Provider { return &Provider{} }

func (p *Provider) Name() string { return "avf" }

func (p *Provider) refresh() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.at.IsZero() && time.Since(p.at) < 2*time.Second {
		return
	}
	p.at = time.Now()
	p.cams, p.mics = devices("vide"), devices("soun")
}

func (p *Provider) Devices() []capture.DeviceInfo {
	p.refresh()
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []capture.DeviceInfo
	for _, d := range p.cams {
		out = append(out, capture.DeviceInfo{ID: d.id, GroupID: d.id, Label: d.name, Kind: capture.VideoInput})
	}
	for _, d := range p.mics {
		out = append(out, capture.DeviceInfo{ID: d.id, GroupID: d.id, Label: d.name, Kind: capture.AudioInput})
	}
	return out
}

// VideoCaps: the session presets, which every camera maps to its nearest native format.
func (p *Provider) VideoCaps(string) (capture.VideoCaps, bool) {
	var c capture.VideoCaps
	for _, s := range [][2]int{{352, 288}, {640, 480}, {1280, 720}, {1920, 1080}} {
		c.Modes = append(c.Modes, capture.Mode{W: s[0], H: s[1], FPSMin: 1, FPSMax: 30})
	}
	return c, true
}

func (p *Provider) AudioCaps(string) (capture.AudioCaps, bool) {
	return capture.AudioCaps{Rates: []int{48000}, MinChannels: 1, MaxChannels: 2, DefaultRate: 48000, DefaultChanns: 1}, true
}

func (p *Provider) has(list []devInfo, id string) (devInfo, bool) {
	for _, d := range list {
		if d.id == id {
			return d, true
		}
	}
	return devInfo{}, false
}

func (p *Provider) OpenVideo(id string) (capture.VideoSource, error) {
	p.refresh()
	p.mu.Lock()
	d, ok := p.has(p.cams, id)
	p.mu.Unlock()
	if !ok {
		return nil, capture.ErrNotFound
	}
	return &camSource{info: capture.DeviceInfo{ID: id, GroupID: id, Label: d.name, Kind: capture.VideoInput}, id: id}, nil
}

func (p *Provider) OpenAudio(id string) (capture.AudioSource, error) {
	p.refresh()
	p.mu.Lock()
	d, ok := p.has(p.mics, id)
	p.mu.Unlock()
	if !ok {
		return nil, capture.ErrNotFound
	}
	return &micSource{info: capture.DeviceInfo{ID: id, GroupID: id, Label: d.name, Kind: capture.AudioInput}, id: id}, nil
}

type camSource struct {
	info capture.DeviceInfo
	id   string
	mu   sync.Mutex
	s    *session
	done chan struct{}
	err  error
	fps  float64
}

func (c *camSource) Info() capture.DeviceInfo { return c.info }
func (c *camSource) Err() error               { c.mu.Lock(); defer c.mu.Unlock(); return c.err }
func (c *camSource) Mode() (int, int, float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.s == nil {
		return 0, 0, 0
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	return c.s.w, c.s.h, c.fps
}

func (c *camSource) Start(vc capture.VideoConstraints) (<-chan *capture.Frame, error) {
	c.mu.Lock()
	if c.s != nil {
		c.mu.Unlock()
		return nil, errors.New("avf: camera already started")
	}
	c.mu.Unlock()
	s, err := startSession(c.id, "vide", vc, 0)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.s, c.fps, c.done = s, 30, make(chan struct{})
	c.mu.Unlock()
	out := make(chan *capture.Frame, 3)
	go func() { // forward and close when stopped
		defer close(out)
		for {
			select {
			case f := <-s.vout:
				push(out, f)
			case <-c.done:
				return
			}
		}
	}()
	return out, nil
}

func (c *camSource) Stop() error {
	c.mu.Lock()
	s, done := c.s, c.done
	c.done = nil
	c.mu.Unlock()
	if s != nil {
		s.stop()
	}
	if done != nil {
		close(done)
	}
	return nil
}

type micSource struct {
	info capture.DeviceInfo
	id   string
	mu   sync.Mutex
	s    *session
	done chan struct{}
	err  error
}

func (m *micSource) Info() capture.DeviceInfo { return m.info }
func (m *micSource) Err() error               { m.mu.Lock(); defer m.mu.Unlock(); return m.err }

// Start delivers the audio in the chunks CoreAudio produces (about 10 to 20 ms); the shared-source layer and the
// consumers cope with any chunk size.
func (m *micSource) Start(c capture.AudioConstraints) (<-chan *capture.AudioChunk, error) {
	m.mu.Lock()
	if m.s != nil {
		m.mu.Unlock()
		return nil, errors.New("avf: microphone already started")
	}
	m.mu.Unlock()
	s, err := startSession(m.id, "soun", capture.VideoConstraints{}, c.Channels)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.s, m.done = s, make(chan struct{})
	m.mu.Unlock()
	out := make(chan *capture.AudioChunk, 16)
	go func() {
		defer close(out)
		for {
			select {
			case a := <-s.aout:
				pushAudio(out, a)
			case <-m.done:
				return
			}
		}
	}()
	return out, nil
}

func (m *micSource) Stop() error {
	m.mu.Lock()
	s, done := m.s, m.done
	m.done = nil
	m.mu.Unlock()
	if s != nil {
		s.stop()
	}
	if done != nil {
		close(done)
	}
	return nil
}
