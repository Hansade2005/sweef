package store

import "time"

// Permission names the site settings keep (they match the engine's names).
const (
	PermNotifications = "notifications"
	PermPeriodicSync  = "periodic-background-sync"
)

// Quiet-UI rules: a site whose prompt the user dismisses this many times in a row stops being asked for a while.
const (
	DismissLimit  = 3
	EmbargoLength = 7 * 24 * time.Hour
)

// Notify returns "granted", "denied" or "" for an origin.
func (s *Sites) Notify(originOrURL string) string { return s.Rule(originOrURL).Notify }

// SetNotify records the user's decision ("granted", "denied") or forgets it (""). A decision resets the dismissal count.
func (s *Sites) SetNotify(originOrURL, state string) {
	o := OriginOf(originOrURL)
	if o == "" || (state != "" && state != "granted" && state != "denied") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rules[o]
	r.Notify, r.NotifyDismissed, r.NotifyEmbargo = state, 0, 0
	s.put(o, r)
	s.save()
}

// SetPeriodic overrides the periodic sync permission of an origin ("granted", "denied", or "" for the default rule).
func (s *Sites) SetPeriodic(originOrURL, state string) {
	o := OriginOf(originOrURL)
	if o == "" || (state != "" && state != "granted" && state != "denied") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rules[o]
	r.Periodic = state
	s.put(o, r)
	s.save()
}

// NoteDismissed counts a prompt the user closed with "Not now". After DismissLimit in a row the origin is embargoed
// for EmbargoLength (no prompt, requests are answered "denied" without asking). It reports whether the embargo began.
func (s *Sites) NoteDismissed(originOrURL string, now time.Time) bool {
	o := OriginOf(originOrURL)
	if o == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rules[o]
	r.NotifyDismissed++
	began := false
	if r.NotifyDismissed >= DismissLimit {
		r.NotifyDismissed, r.NotifyEmbargo, began = 0, now.Add(EmbargoLength).Unix(), true
	}
	s.put(o, r)
	s.save()
	return began
}

// Embargoed reports whether prompts for the origin are suppressed at now.
func (s *Sites) Embargoed(originOrURL string, now time.Time) bool {
	return s.Rule(originOrURL).NotifyEmbargo > now.Unix()
}

// WithPermissions lists the origins that have any web permission decision, for the settings page.
func (s *Sites) WithPermissions() map[string]SiteRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]SiteRule{}
	for o, r := range s.rules {
		if r.Notify != "" || r.Periodic != "" || r.NotifyEmbargo != 0 {
			out[o] = r
		}
	}
	return out
}
