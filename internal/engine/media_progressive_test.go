package engine

// Progressive <video>/<audio> playback through the native player: sound reaches the audio output, the
// volume/mute/rate/autoplay rules hold, and the tab reports that it is audible.

import (
	"math"
	"os"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/audioout"
	"github.com/sweefbrowser/sweef/internal/media"
)

func peakOf(pcm []float32) float32 {
	var pk float32
	for _, v := range pcm {
		if v < 0 {
			v = -v
		}
		if v > pk {
			pk = v
		}
	}
	return pk
}

// recordedTail is the left channel recorded after frame `from`.
func recordedTail(sink *audioout.TestSink, from int) []float32 {
	l := leftChannel(sink.Recorded())
	if from >= len(l) {
		return nil
	}
	return l[from:]
}

func TestProgressiveVideoPlaysWithSoundVolumeAndMute(t *testing.T) {
	_, sink := useRecordingSink(t)
	tab := newMediaTab(t)
	evalJSON(t, tab, `(function(){var v=document.createElement('video');v.id='v';v.width=160;v.height=120;v.src='clip.mp4';v.loop=true;document.body.appendChild(v);return 1})()`, nil)
	waitTrue(t, tab, "document.getElementById('v').readyState >= 3", 5*time.Second)

	// no gesture yet: unmuted playback is refused
	var name string
	evalJSON(t, tab, "document.getElementById('v').play().then(function(){return 'resolved'},function(e){return e.name})", &name)
	if name != "NotAllowedError" {
		t.Fatalf("play() before a gesture: %q", name)
	}
	if sink.Frames() > 0 && peakOf(leftChannel(sink.Recorded())) > 0 {
		t.Fatal("sound before any playback")
	}
	if err := tab.Click("#v"); err != nil {
		t.Fatal(err)
	}
	evalJSON(t, tab, "document.getElementById('v').play().then(function(){return 'resolved'},function(e){return e.name})", &name)
	if name != "resolved" {
		t.Fatalf("play() after a click: %q", name)
	}
	waitTrue(t, tab, "document.getElementById('v').currentTime > 0.6", 4*time.Second)
	rec := leftChannel(sink.Recorded())
	if pk := peakOf(rec); pk < 0.05 {
		t.Fatalf("playing but the audio output is silent: peak %v", pk)
	}
	// the clock follows the audio device: currentTime and the device agree
	ct := jsNum(t, tab, "document.getElementById('v').currentTime")
	heard := float64(sink.Frames()) / audioout.DeviceRate
	first := firstSound(sink.Recorded())
	if first < 0 {
		t.Fatal("no first sound")
	}
	played := heard - float64(first)/audioout.DeviceRate
	if math.Abs(ct-played) > 0.25 {
		t.Errorf("currentTime %.3f vs %.3f s of sound played", ct, played)
	}

	// volume halves the level
	from := sink.Frames() + 4800
	evalJSON(t, tab, "document.getElementById('v').volume=1;1", nil)
	tab.Settle(400 * time.Millisecond)
	full := peakOf(recordedTail(sink, from))
	from = sink.Frames() + 4800
	evalJSON(t, tab, "document.getElementById('v').volume=0.25;1", nil)
	tab.Settle(400 * time.Millisecond)
	quiet := peakOf(recordedTail(sink, from))
	if full < 0.05 || quiet > full*0.34 || quiet < full*0.18 {
		t.Errorf("volume 0.25: peak %.3f vs %.3f at full volume", quiet, full)
	}
	// muted: silent, but the clock goes on
	evalJSON(t, tab, "document.getElementById('v').volume=1;document.getElementById('v').muted=true;1", nil)
	tab.Settle(100 * time.Millisecond)
	t1 := jsNum(t, tab, "document.getElementById('v').currentTime")
	from = sink.Frames() + 2400
	tab.Settle(500 * time.Millisecond)
	if pk := peakOf(recordedTail(sink, from)); pk > 0.001 {
		t.Errorf("muted element still audible: %v", pk)
	}
	t2 := jsNum(t, tab, "document.getElementById('v').currentTime")
	if d := t2 - t1; d < 0.35 && !(t1 > 1.4) { // may wrap at the loop
		t.Errorf("clock advanced only %.2f s in 0.5 s while muted", d)
	}
	// playback rate: pitch follows, the clock runs twice as fast
	evalJSON(t, tab, "document.getElementById('v').muted=false;1", nil)
	var after float64
	evalJSON(t, tab, "document.getElementById('v').pause();document.getElementById('v').currentTime=0;document.getElementById('v').playbackRate=2;document.getElementById('v').play();document.getElementById('v').currentTime", &after)
	tab.Settle(100 * time.Millisecond)
	a := jsNum(t, tab, "document.getElementById('v').currentTime")
	tab.Settle(500 * time.Millisecond)
	b := jsNum(t, tab, "document.getElementById('v').currentTime")
	if r := (b - a) / 0.5; r < 1.6 || r > 2.4 {
		if b > a { // wrap-around at the loop point can hide it
			t.Errorf("playbackRate 2: the clock advanced %.2f s per s", r)
		}
	}
}

func TestTabAudibleAndMuteEvents(t *testing.T) {
	out, sink := useRecordingSink(t)
	tab := newMediaTab(t)
	_, ch := tab.eng.Bus().Subscribe(256)
	evalJSON(t, tab, `(function(){var v=document.createElement('video');v.id='v';v.width=160;v.height=120;v.src='clip.mp4';v.loop=true;document.body.appendChild(v);return 1})()`, nil)
	waitTrue(t, tab, "document.getElementById('v').readyState >= 3", 5*time.Second)
	_ = tab.Click("#v")
	evalJSON(t, tab, "document.getElementById('v').play()", nil)
	waitTrue(t, tab, "document.getElementById('v').currentTime > 0.3", 4*time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for !tab.AudioState().Audible && time.Now().Before(deadline) {
		out.Poll()
		tab.Settle(50 * time.Millisecond)
	}
	if !tab.AudioState().Audible {
		t.Fatal("the tab never became audible")
	}
	sawAudible := false
drain:
	for {
		select {
		case ev := <-ch:
			if ev.Kind == EvAudio && ev.Data.(AudioState).Audible {
				sawAudible = true
			}
		default:
			break drain
		}
	}
	if !sawAudible {
		t.Error("no audible event on the bus")
	}
	// tab mute: silent, still marked audible, the video keeps its clock
	tab.SetAudioMuted(true)
	tab.Settle(100 * time.Millisecond)
	from := sink.Frames() + 4800
	t1 := jsNum(t, tab, "document.getElementById('v').currentTime")
	tab.Settle(500 * time.Millisecond)
	if pk := peakOf(recordedTail(sink, from)); pk > 0.001 {
		t.Errorf("muted tab is audible in the mix: %v", pk)
	}
	if !tab.AudioState().Muted || !tab.AudioState().Audible {
		t.Errorf("state %+v, want muted and still audible", tab.AudioState())
	}
	if t2 := jsNum(t, tab, "document.getElementById('v').currentTime"); t2 == t1 {
		t.Error("the clock stopped while the tab was muted")
	}
	tab.SetAudioMuted(false)
	tab.Settle(100 * time.Millisecond)
	from = sink.Frames() + 4800
	tab.Settle(400 * time.Millisecond)
	if pk := peakOf(recordedTail(sink, from)); pk < 0.05 {
		t.Errorf("unmuted tab is silent: %v", pk)
	}
	// pausing ends the sound; the indicator goes out after its hold time
	evalJSON(t, tab, "document.getElementById('v').pause()", nil)
	deadline = time.Now().Add(4 * time.Second)
	for tab.AudioState().Audible && time.Now().Before(deadline) {
		out.Poll()
		tab.Settle(100 * time.Millisecond)
	}
	if tab.AudioState().Audible {
		t.Error("the tab stayed audible after pause()")
	}
}

func TestMutedVideoAutoplaysAndAudioDoesNot(t *testing.T) {
	useRecordingSink(t)
	tab := newMediaTab(t)
	var r map[string]string
	evalJSON(t, tab, `(function(){
	  var v=document.createElement('video');v.muted=true;v.src='clip.mp4';document.body.appendChild(v);
	  var a=document.createElement('audio');a.muted=true;a.src='tone.wav';document.body.appendChild(a);
	  var o={};
	  var pv=v.play().then(function(){o.video='resolved'},function(e){o.video=e.name});
	  var pa=a.play().then(function(){o.audio='resolved'},function(e){o.audio=e.name});
	  return Promise.all([pv,pa]).then(function(){return o});
	})()`, &r)
	if r["video"] != "resolved" {
		t.Errorf("muted video autoplay: %q", r["video"])
	}
	if r["audio"] != "NotAllowedError" { // Chromium only exempts muted videos
		t.Errorf("muted audio element: %q, want NotAllowedError", r["audio"])
	}
}

func TestWebAudioDestinationReachesTheOutput(t *testing.T) {
	_, sink := useRecordingSink(t)
	tab := newMediaTab(t)
	evalJSON(t, tab, `(function(){var b=document.createElement('button');b.id='b';b.textContent='go';document.body.appendChild(b);return 1})()`, nil)
	if err := tab.Click("#b"); err != nil { // a real click: the page may start audio
		t.Fatal(err)
	}
	evalJSON(t, tab, `(function(){
	  window.ctx = new AudioContext();
	  var o = ctx.createOscillator(), g = ctx.createGain();
	  o.frequency.value = 660; g.gain.value = 0.4;
	  o.connect(g); g.connect(ctx.destination);
	  return ctx.resume().then(function(){ o.start(); return ctx.state; });
	})()`, nil)
	for i := 0; i < 40; i++ { // the graph renders as the page's timers and clock reads catch it up
		tab.Settle(20 * time.Millisecond)
		time.Sleep(20 * time.Millisecond)
		jsNum(t, tab, "ctx.currentTime")
	}
	rec := leftChannel(sink.Recorded())
	i0 := firstSound(sink.Recorded())
	if i0 < 0 || len(rec) < i0+24000 {
		t.Fatalf("Web Audio produced no sound at the output (first sound %d of %d frames)", i0, len(rec))
	}
	if got := hzOf(rec[i0+2400:i0+24000], 48000); math.Abs(got-660) > 25 {
		t.Errorf("heard %.0f Hz, want 660", got)
	}
	if pk := peakOf(rec[i0 : i0+24000]); pk < 0.3 || pk > 0.5 {
		t.Errorf("level %.3f, want about 0.4", pk)
	}
	evalJSON(t, tab, "ctx.close().then(function(){return 1})", nil)
}

// Every audio format the engine can decode plays through the output at the right pitch.
func TestAudioElementFormatsReachTheOutput(t *testing.T) {
	files := []string{"tone.wav", "tone.flac", "tone.aac", "tone.mp3", "tone.ogg", "opus_st.webm", "opus_st.mp4", "aac_st_ms.m4a"}
	for _, f := range files {
		f := f
		t.Run(f, func(t *testing.T) {
			_, sink := useRecordingSink(t)
			tab := newMediaTab(t)
			evalJSON(t, tab, `(function(){var a=document.createElement('audio');a.id='a';a.src='`+f+`';document.body.appendChild(a);var b=document.createElement('button');b.id='b';b.textContent='x';document.body.appendChild(b);return 1})()`, nil)
			waitTrue(t, tab, "document.getElementById('a').readyState >= 3", 5*time.Second)
			if err := tab.Click("#b"); err != nil {
				t.Fatal(err)
			}
			var res string
			evalJSON(t, tab, "document.getElementById('a').play().then(function(){return 'ok'},function(e){return e.name})", &res)
			if res != "ok" {
				t.Fatalf("play(): %s", res)
			}
			waitTrue(t, tab, "document.getElementById('a').currentTime > 0.8", 8*time.Second)
			i0 := firstSound(sink.Recorded())
			left := leftChannel(sink.Recorded())
			if i0 < 0 || len(left) < i0+24000 {
				t.Fatalf("no sound at the output: first %d of %d frames", i0, len(left))
			}
			// the pitch of what was played against the pitch of the decoded file
			data, err := os.ReadFile("testdata/media/" + f)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := media.DecodeAudio(data)
			if err != nil {
				t.Fatal(err)
			}
			want := hzOf(ref.Data[0][ref.SampleRate/10:ref.SampleRate/2], ref.SampleRate)
			got := hzOf(left[i0+4800:i0+24000], 48000)
			if math.Abs(got-want) > want*0.05 {
				t.Errorf("heard %.0f Hz, the file decodes to %.0f Hz", got, want)
			}
			// the element clock follows the sound that was actually played
			ct := jsNum(t, tab, "document.getElementById('a').currentTime")
			played := float64(sink.Frames()-i0) / audioout.DeviceRate
			if math.Abs(ct-played) > 0.3 {
				t.Errorf("currentTime %.2f vs %.2f s of sound played", ct, played)
			}
		})
	}
}
