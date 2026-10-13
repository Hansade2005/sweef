// Command capdemo lists the capture devices of this machine and records from one of them, so the hardware paths
// can be checked without a browser:
//
//	go run ./internal/capture/cmd/capdemo -list
//	go run ./internal/capture/cmd/capdemo -video 0 -frames 30 -size 1280x720 -fps 30 -out /tmp/cap
//	go run ./internal/capture/cmd/capdemo -audio 0 -seconds 5 -out /tmp/cap
//	go run ./internal/capture/cmd/capdemo -fake -video 0 -frames 5 -out /tmp/cap     (synthetic devices, no hardware)
//
// A device is chosen by its position in the -list output or by its id. Video is written as PNG files
// (frame-0001.png, ...) plus a one-line summary of the measured frame rate; audio as a 16-bit PCM WAV file.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
	_ "github.com/sweefbrowser/sweef/internal/capture/platform"
	_ "github.com/sweefbrowser/sweef/internal/capture/synthetic"
)

func main() {
	list := flag.Bool("list", false, "list the devices and exit")
	fake := flag.Bool("fake", false, "enable the synthetic devices (SWEEF_FAKE_DEVICES=1)")
	video := flag.String("video", "", "record frames from this camera (index in -list or id)")
	audio := flag.String("audio", "", "record from this microphone (index in -list or id)")
	frames := flag.Int("frames", 30, "number of video frames to record")
	every := flag.Int("every", 1, "write every Nth frame as a PNG (all frames are still counted)")
	seconds := flag.Float64("seconds", 3, "seconds of audio to record")
	size := flag.String("size", "", "requested video size, e.g. 1280x720")
	fps := flag.Float64("fps", 0, "requested frame rate")
	out := flag.String("out", ".", "output directory")
	flag.Parse()
	if *fake {
		os.Setenv("SWEEF_FAKE_DEVICES", "1")
	}
	cams, mics := capture.Devices(capture.VideoInput), capture.Devices(capture.AudioInput)
	if *list || (*video == "" && *audio == "") {
		fmt.Println("cameras:")
		for i, d := range cams {
			caps := capture.VideoCapsOf(d.ID)
			fmt.Printf("  [%d] %s\n      id=%s group=%s\n", i, d.Label, d.ID, d.GroupID)
			for _, m := range caps.Modes {
				fmt.Printf("      %dx%d  %.0f-%.0f fps\n", m.W, m.H, m.FPSMin, m.FPSMax)
			}
		}
		if len(cams) == 0 {
			fmt.Println("  (none)")
		}
		fmt.Println("microphones:")
		for i, d := range mics {
			c := capture.AudioCapsOf(d.ID)
			fmt.Printf("  [%d] %s\n      id=%s group=%s  rates=%v channels=%d-%d\n", i, d.Label, d.ID, d.GroupID, c.Rates, c.MinChannels, c.MaxChannels)
		}
		if len(mics) == 0 {
			fmt.Println("  (none)")
		}
		return
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err)
	}
	if *video != "" {
		id := pick(*video, cams)
		var vc capture.VideoConstraints
		vc.FrameRate = *fps
		if *size != "" {
			parts := strings.SplitN(strings.ToLower(*size), "x", 2)
			if len(parts) == 2 {
				vc.Width, _ = strconv.Atoi(parts[0])
				vc.Height, _ = strconv.Atoi(parts[1])
			}
		}
		recordVideo(id, vc, *frames, *every, *out)
	}
	if *audio != "" {
		recordAudio(pick(*audio, mics), *seconds, *out)
	}
}

func pick(sel string, ds []capture.DeviceInfo) string {
	if n, err := strconv.Atoi(sel); err == nil && n >= 0 && n < len(ds) {
		return ds[n].ID
	}
	return sel
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "capdemo:", err)
	os.Exit(1)
}

func recordVideo(id string, vc capture.VideoConstraints, n, every int, dir string) {
	l, err := capture.AcquireVideo(id, vc, 10*time.Second)
	if err != nil {
		fatal(fmt.Errorf("open camera: %w", err))
	}
	defer l.Release()
	m := l.Mode()
	fmt.Printf("camera %s: %dx%d at %.1f fps\n", id, m.W, m.H, m.FPS)
	sub := l.Subscribe()
	defer sub.Cancel()
	var first, last time.Duration
	got, written := 0, 0
	t0 := time.Now()
	for f := range sub.C {
		if got == 0 {
			first = f.PTS
		}
		last = f.PTS
		if got%max(every, 1) == 0 {
			p := filepath.Join(dir, fmt.Sprintf("frame-%04d.png", got+1))
			w, err := os.Create(p)
			if err != nil {
				fatal(err)
			}
			if err := png.Encode(w, f.ToRGBA()); err != nil {
				fatal(err)
			}
			w.Close()
			written++
		}
		got++
		if got >= n {
			break
		}
	}
	wall := time.Since(t0).Seconds()
	fmt.Printf("received %d frames (%d PNGs written to %s) in %.2f s of wall time", got, written, dir, wall)
	if got > 1 && last > first {
		fmt.Printf("; device timestamps give %.1f fps", float64(got-1)/(last-first).Seconds())
	}
	fmt.Println()
	if got < n {
		if err := l.Err(); err != nil {
			fatal(fmt.Errorf("the camera stopped after %d frames: %w", got, err))
		}
		fatal(fmt.Errorf("the camera stopped after %d frames", got))
	}
}

func recordAudio(id string, secs float64, dir string) {
	l, err := capture.AcquireAudio(id, capture.AudioConstraints{Channels: 1}, 10*time.Second)
	if err != nil {
		fatal(fmt.Errorf("open microphone: %w", err))
	}
	defer l.Release()
	m := l.Mode()
	fmt.Printf("microphone %s: %d Hz, %d channel(s)\n", id, m.Rate, m.Channels)
	sub := l.Subscribe()
	defer sub.Cancel()
	var pcm []int16
	want := int(secs * float64(m.Rate) * float64(m.Channels))
	for c := range sub.C {
		pcm = append(pcm, c.PCM...)
		if len(pcm) >= want {
			break
		}
	}
	if len(pcm) < want {
		if err := l.Err(); err != nil {
			fatal(fmt.Errorf("the microphone stopped: %w", err))
		}
	}
	var peak, sumsq float64
	for _, s := range pcm {
		v := float64(s)
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
		sumsq += v * v
	}
	p := filepath.Join(dir, "capture.wav")
	if err := writeWAV(p, pcm, m.Rate, m.Channels); err != nil {
		fatal(err)
	}
	rms := 0.0
	if len(pcm) > 0 {
		rms = math.Sqrt(sumsq / float64(len(pcm)))
	}
	fmt.Printf("wrote %s: %d samples (%.2f s), peak %.0f / 32767, rms %.0f\n", p, len(pcm), float64(len(pcm))/float64(m.Rate*m.Channels), peak, rms)
}

func writeWAV(path string, pcm []int16, rate, ch int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data := len(pcm) * 2
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+data))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], uint16(ch))
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*ch*2))
	binary.LittleEndian.PutUint16(h[32:], uint16(ch*2))
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(data))
	if _, err := f.Write(h); err != nil {
		return err
	}
	buf := make([]byte, data)
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(buf[2*i:], uint16(s))
	}
	_, err = f.Write(buf)
	return err
}
