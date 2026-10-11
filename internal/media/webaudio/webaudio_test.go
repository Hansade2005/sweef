package webaudio

import (
	"math"
	"testing"
)

func newOff(sr float64, frames int) *Context { return NewContext(sr, true, 1, frames) }

func must(t *testing.T, id int, err error) int {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestOscillatorSine(t *testing.T) {
	c := newOff(44100, 4410)
	osc := must(t, 0, nil)
	osc, _ = c.AddNode("oscillator", nil)
	c.Connect(osc, 0, c.Dest.ID, 0)
	c.Start(osc, 0, 0, 0, false)
	c.Render(4410)
	var maxErr float64
	for i, v := range c.Output[0] {
		want := math.Sin(2 * math.Pi * 440 * float64(i) / 44100)
		maxErr = math.Max(maxErr, math.Abs(float64(v)-want))
	}
	t.Logf("sine max err %.3g", maxErr)
	if maxErr > 1e-5 {
		t.Fatalf("sine error %g", maxErr)
	}
}

func TestOscillatorStartStop(t *testing.T) {
	c := newOff(44100, 1280)
	osc, _ := c.AddNode("oscillator", nil)
	c.Connect(osc, 0, c.Dest.ID, 0)
	c.Start(osc, 128.0/44100, 0, 0, false)
	c.Stop(osc, 640.0/44100)
	c.Render(1280)
	for i, v := range c.Output[0] {
		if (i < 128 || i >= 640) && v != 0 {
			t.Fatalf("sample %d should be silent: %g", i, v)
		}
	}
	if c.Output[0][200] == 0 {
		t.Fatal("expected sound in window")
	}
	if len(c.TakeEnded()) != 1 {
		t.Fatal("ended not reported")
	}
}

func TestParamAutomation(t *testing.T) {
	c := newOff(1000, 1000)
	cs, _ := c.AddNode("constantsource", nil)
	g, _ := c.AddNode("gain", nil)
	c.Connect(cs, 0, g, 0)
	c.Connect(g, 0, c.Dest.ID, 0)
	c.Start(cs, 0, 0, 0, false)
	p := c.Node(g).Params["gain"]
	p.Value = 0
	p.AddEvent(Event{Type: EvSetValue, Time: 0, Value: 0})
	p.AddEvent(Event{Type: EvLinear, Time: 0.5, Value: 1})
	p.AddEvent(Event{Type: EvExponential, Time: 0.75, Value: 4})
	p.AddEvent(Event{Type: EvSetTarget, Time: 0.8, Value: 0, TimeCst: 0.05})
	c.Render(1000)
	o := c.Output[0]
	check := func(i int, want float64) {
		if math.Abs(float64(o[i])-want) > 1e-5 {
			t.Errorf("frame %d = %g want %g", i, o[i], want)
		}
	}
	check(250, 0.5)
	check(500, 1)
	check(625, 2) // exponential 1 -> 4 halfway = 2
	check(750, 4)
	check(800, 4)
	check(900, 4*math.Exp(-0.1/0.05))
}

func TestBiquadLowpassResponse(t *testing.T) {
	c := newOff(44100, 44100)
	osc, _ := c.AddNode("oscillator", nil)
	c.Node(osc).Params["frequency"].Value = 2000
	bq, _ := c.AddNode("biquad", nil)
	c.Node(bq).Params["frequency"].Value = 2000
	c.Node(bq).Params["Q"].Value = -3.0103 // Q in dB, ~0.707 linear -> -3 dB at fc
	c.Connect(osc, 0, bq, 0)
	c.Connect(bq, 0, c.Dest.ID, 0)
	c.Start(osc, 0, 0, 0, false)
	c.Render(44100)
	var peak float64
	for _, v := range c.Output[0][20000:] {
		peak = math.Max(peak, math.Abs(float64(v)))
	}
	mag, _ := c.FrequencyResponse(bq, []float64{2000, 100, 30000})
	t.Logf("measured gain %.4f analytic %.4f, nan=%v", peak, mag[0], math.IsNaN(mag[2]))
	if math.Abs(peak-mag[0]) > 5e-3 || math.Abs(mag[0]-0.7071) > 0.02 || !math.IsNaN(mag[2]) {
		t.Fatalf("lowpass response off: %v %v", peak, mag)
	}
}

func TestDelayAndConvolver(t *testing.T) {
	c := newOff(44100, 1024)
	// impulse buffer
	imp := make([]float32, 128)
	imp[0] = 1
	c.SetBuffer(1, 44100, [][]float32{imp})
	src, _ := c.AddNode("buffersource", nil)
	c.Set(src, "buffer", 1.0)
	d, _ := c.AddNode("delay", map[string]any{"maxDelayTime": 1.0})
	c.Node(d).Params["delayTime"].Value = 200.0 / 44100
	c.Connect(src, 0, d, 0)
	c.Connect(d, 0, c.Dest.ID, 0)
	c.Start(src, 0, 0, 0, false)
	c.Render(1024)
	for i, v := range c.Output[0] {
		if (i == 200) != (v > 0.99) || (i != 200 && v != 0 && math.Abs(float64(v)) > 1e-4) {
			t.Fatalf("delay impulse at %d: %g", i, v)
		}
	}

	c2 := newOff(44100, 2048)
	ir := make([]float32, 700)
	ir[300] = 0.5
	c2.SetBuffer(2, 44100, [][]float32{ir})
	c2.SetBuffer(1, 44100, [][]float32{imp})
	s2, _ := c2.AddNode("buffersource", nil)
	c2.Set(s2, "buffer", 1.0)
	cv, _ := c2.AddNode("convolver", nil)
	c2.Set(cv, "normalize", 0.0)
	c2.Set(cv, "buffer", 2.0)
	c2.Connect(s2, 0, cv, 0)
	c2.Connect(cv, 0, c2.Dest.ID, 0)
	c2.Start(s2, 0, 0, 0, false)
	c2.Render(2048)
	if math.Abs(float64(c2.Output[0][300])-0.5) > 1e-5 {
		t.Fatalf("convolver impulse response misplaced: %v", c2.Output[0][295:305])
	}
}

func TestAnalyserPeak(t *testing.T) {
	c := NewContext(44100, false, 2, 0)
	osc, _ := c.AddNode("oscillator", nil)
	c.Node(osc).Params["frequency"].Value = 100 * 44100.0 / 1024 // bin 100 of 1024-point
	an, _ := c.AddNode("analyser", nil)
	c.Set(an, "fftSize", 1024.0)
	c.Set(an, "smoothing", 0.0)
	c.Connect(osc, 0, an, 0)
	c.Connect(an, 0, c.Dest.ID, 0)
	c.Start(osc, 0, 0, 0, false)
	c.Render(8192)
	f := c.AnalyserFloatFreq(an)
	peak := 0
	for i, v := range f {
		if v > f[peak] {
			peak = i
		}
	}
	b := c.AnalyserByteFreq(an)
	t.Logf("peak bin %d dB %.2f byte %d", peak, f[peak], b[peak])
	if peak != 100 || len(f) != 512 {
		t.Fatalf("peak bin %d", peak)
	}
	// Blackman window coherent gain 0.42 * 0.5 amplitude -> -13.5 dB at the peak bin
	if math.Abs(float64(f[peak])-(20*math.Log10(0.5*0.42))) > 0.2 {
		t.Fatalf("peak magnitude %.2f dB", f[peak])
	}
}

func TestExternalStepping(t *testing.T) {
	c := newOff(44100, 256)
	cs, _ := c.AddNode("constantsource", nil)
	ex, _ := c.AddNode("external", map[string]any{"inputs": 1.0, "outputs": 1.0})
	c.Connect(cs, 0, ex, 0)
	c.Connect(ex, 0, c.Dest.ID, 0)
	c.Start(cs, 0, 0, 0, false)
	for c.Frame < 256 {
		for {
			n, in := c.Step()
			if n == nil {
				break
			}
			out := make([]float32, Quantum)
			for i := range out {
				out[i] = in[0][0][i] * 3
			}
			c.Provide(n, [][]float32{out})
		}
	}
	if c.Output[0][0] != 3 || c.Output[0][255] != 3 {
		t.Fatalf("external output %v", c.Output[0][:3])
	}
}

func TestWaveShapeAndPan(t *testing.T) {
	if shape([]float32{-1, 0, 1}, 0.5) != 0.5 || shape([]float32{-1, 0, 1}, 3) != 1 {
		t.Fatal("shape")
	}
	c := NewContext(44100, true, 2, 128)
	cs, _ := c.AddNode("constantsource", nil)
	p, _ := c.AddNode("stereopanner", nil)
	c.Node(p).Params["pan"].Value = 1
	c.Connect(cs, 0, p, 0)
	c.Connect(p, 0, c.Dest.ID, 0)
	c.Start(cs, 0, 0, 0, false)
	c.Render(128)
	if math.Abs(float64(c.Output[0][5])) > 1e-6 || math.Abs(float64(c.Output[1][5])-1) > 1e-6 {
		t.Fatalf("hard right: %g %g", c.Output[0][5], c.Output[1][5])
	}
}
