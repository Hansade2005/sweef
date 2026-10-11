package hevc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/yuv"
)

func haveFFmpeg() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// frameBytes serialises a frame like ffmpeg's rawvideo output.
func frameBytes(f *yuv.Frame) []byte {
	var out []byte
	put := func(pl []uint16, stride, w, h int) {
		for y := 0; y < h; y++ {
			row := pl[y*stride : y*stride+w]
			if f.Depth == 8 {
				for _, v := range row {
					out = append(out, byte(v))
				}
			} else {
				for _, v := range row {
					out = binary.LittleEndian.AppendUint16(out, v)
				}
			}
		}
	}
	put(f.Planes[0], f.Stride[0], f.Width, f.Height)
	if f.Format != yuv.F400 {
		put(f.Planes[1], f.Stride[1], f.CWidth, f.CHeight)
		put(f.Planes[2], f.Stride[2], f.CWidth, f.CHeight)
	}
	return out
}

func decodeAnnexB(t testing.TB, data []byte) ([]*yuv.Frame, error) {
	d := NewDecoder()
	var out []*yuv.Frame
	fr, err := d.Push(data)
	out = append(out, fr...)
	out = append(out, d.Flush()...)
	return out, err
}

func ffmpegRaw(t testing.TB, path, pixFmt string) []byte {
	t.Helper()
	args := []string{"-v", "error", "-nostdin", "-i", path, "-fps_mode", "passthrough", "-f", "rawvideo"}
	if pixFmt != "" {
		args = append(args, "-pix_fmt", pixFmt)
	}
	args = append(args, "-")
	cmd := exec.Command("ffmpeg", args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, se.String())
	}
	return so.Bytes()
}

// compareFile decodes an Annex B file natively and compares with ffmpeg.
func compareFile(t *testing.T, path, pixFmt string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ref := ffmpegRaw(t, path, pixFmt)
	frames, derr := decodeAnnexB(t, data)
	if derr != nil {
		t.Logf("decode error: %v", derr)
	}
	if len(frames) == 0 {
		t.Fatalf("no frames decoded (err %v)", derr)
	}
	var got []byte
	fsz := len(frameBytes(frames[0]))
	for _, f := range frames {
		got = append(got, frameBytes(f)...)
	}
	if len(ref) != len(got) {
		t.Errorf("size: ffmpeg %d bytes (%d frames) vs native %d bytes (%d frames)", len(ref), len(ref)/fsz, len(got), len(frames))
	}
	n := len(ref)
	if len(got) < n {
		n = len(got)
	}
	bad := 0
	for i := 0; i+fsz <= n; i += fsz {
		if !bytes.Equal(ref[i:i+fsz], got[i:i+fsz]) {
			if bad < 3 {
				j := 0
				for ref[i+j] == got[i+j] {
					j++
				}
				f := frames[i/fsz]
				bps := 1
				if f.Depth > 8 {
					bps = 2
				}
				pos := j / bps
				plane, x, y := "Y", pos%f.Width, pos/f.Width
				if pos >= f.Width*f.Height {
					pos -= f.Width * f.Height
					plane = "U"
					if f.CWidth > 0 && pos >= f.CWidth*f.CHeight {
						pos -= f.CWidth * f.CHeight
						plane = "V"
					}
					x, y = pos%f.CWidth, pos/f.CWidth
				}
				t.Errorf("frame %d differs first at %s (%d,%d): ffmpeg %d native %d", i/fsz, plane, x, y, ref[i+j], got[i+j])
			}
			bad++
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d frames differ", bad, len(ref)/fsz)
	}
}

type genOpts struct {
	src    string // lavfi source expression
	frames int
	pixFmt string
	x265   string
	extra  []string
}

func defaultSrc(size string) string {
	return fmt.Sprintf("testsrc2=size=%s:rate=24,noise=alls=12:allf=t", size)
}

func genClip(t testing.TB, name string, o genOpts) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, name)
	if o.src == "" {
		o.src = defaultSrc("320x180")
	}
	if o.frames == 0 {
		o.frames = 12
	}
	args := []string{"-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", o.src, "-frames:v", fmt.Sprint(o.frames)}
	if o.pixFmt != "" {
		args = append(args, "-pix_fmt", o.pixFmt)
	}
	args = append(args, "-c:v", "libx265", "-x265-params", "log-level=error:"+o.x265)
	args = append(args, o.extra...)
	args = append(args, out)
	if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v %s", args, err, b)
	}
	return out
}

type variant struct {
	name string
	o    genOpts
}

func runVariants(t *testing.T, vs []variant) {
	if !haveFFmpeg() {
		t.Skip("no ffmpeg")
	}
	for _, v := range vs {
		v := v
		t.Run(v.name, func(t *testing.T) {
			path := genClip(t, "c.hevc", v.o)
			pf := v.o.pixFmt
			if pf == "" {
				pf = "yuv420p"
			}
			compareFile(t, path, pf)
		})
	}
}

func TestIntraBasic(t *testing.T) {
	runVariants(t, []variant{
		{"intra", genOpts{x265: "keyint=1:ctu=64", frames: 3}},
		{"intra32", genOpts{x265: "keyint=1:ctu=32", frames: 3}},
		{"intra16", genOpts{x265: "keyint=1:ctu=16", frames: 3}},
	})
}

func TestInterBasic(t *testing.T) {
	runVariants(t, []variant{
		{"default", genOpts{x265: ""}},
		{"nobframes", genOpts{x265: "bframes=0"}},
	})
}

func init() {
	_ = strings.TrimSpace
}
