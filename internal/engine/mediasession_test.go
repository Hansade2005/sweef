package engine

import (
	"testing"
	"time"
)

// navigator.mediaSession reaches the embedding app, and the app's buttons run the page's action handlers.
func TestMediaSessionReachesTheApp(t *testing.T) {
	tab := platformTab(t)
	_, ch := tab.eng.Bus().Subscribe(64)
	evalJSON(t, tab, `(function(){
	  window.log = [];
	  navigator.mediaSession.metadata = new MediaMetadata({title:'Song', artist:'Band', album:'LP', artwork:[{src:'/a.png', sizes:'96x96', type:'image/png'}]});
	  navigator.mediaSession.playbackState = 'playing';
	  navigator.mediaSession.setPositionState({duration: 200, position: 12, playbackRate: 1});
	  ['play','pause','nexttrack','seekto'].forEach(function(a){ navigator.mediaSession.setActionHandler(a, function(d){ log.push(a + ':' + (d.seekTime === undefined ? '' : d.seekTime)); }); });
	  return 1; })()`, nil)
	tab.Settle(30 * time.Millisecond)
	si := tab.MediaSession()
	if !si.Active || si.Title != "Song" || si.Artist != "Band" || si.Album != "LP" || si.PlaybackState != "playing" || si.Duration != 200 || si.Position != 12 ||
		len(si.Artwork) != 1 || si.Artwork[0].Sizes != "96x96" || len(si.Actions) != 4 {
		t.Fatalf("session: %+v", si)
	}
	seen := false
drain:
	for {
		select {
		case ev := <-ch:
			if v, ok := ev.Data.(MediaSessionInfo); ev.Kind == EvMediaSession && ok && v.Title == "Song" {
				seen = true
			}
		default:
			break drain
		}
	}
	if !seen {
		t.Error("no EvMediaSession on the bus")
	}
	if !tab.MediaSessionAction("nexttrack", 0) || !tab.MediaSessionAction("seekto", 42) {
		t.Fatal("handled actions were refused")
	}
	if tab.MediaSessionAction("previoustrack", 0) {
		t.Error("an action without a handler (and no media element) was reported handled")
	}
	if tab.MediaSessionAction("bogus", 0) {
		t.Error("unknown action accepted")
	}
	var log string
	evalJSON(t, tab, `log.join()`, &log)
	if log != "nexttrack:,seekto:42" {
		t.Errorf("handlers ran: %q", log)
	}
	// a metadata field changed in place is published again
	_, _ = tab.Evaluate(`navigator.mediaSession.metadata.title = 'Other'; 1`)
	tab.Settle(30 * time.Millisecond)
	if got := tab.MediaSession().Title; got != "Other" {
		t.Errorf("title after in-place change: %q", got)
	}
}
