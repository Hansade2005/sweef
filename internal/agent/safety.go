package agent

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/net/publicsuffix"
)

// safety.go - prompt-injection defences and action policy.
//
// Layers (none of them relies on the model behaving):
//  1. Everything that originates in a page is wrapped in an untrusted
//     delimiter and the system prompt says it is data, not instructions.
//     The delimiter cannot be closed from inside the page (escapeUntrusted).
//  2. Risky actions (payment, outgoing messages, deletion, credential changes,
//     and navigation to a different registrable domain after untrusted text
//     was read) require Backend.Confirm, enforced in code before the step runs.
//  3. Internal pages (chrome://, sweef://, file:, javascript:, ...) can be
//     neither navigated to nor observed.
//  4. Per-run limits on steps, navigations, turns, tokens and cost.
//  5. Pages the user marked not-allowed are never sent to the model.

// ---- untrusted wrapper ------------------------------------------------------

const (
	untrustedOpen  = "<untrusted_page_content"
	untrustedClose = "</untrusted_page_content>"
)

var attrClean = strings.NewReplacer("\"", "'", "\n", " ", "\r", " ", "<", "(", ">", ")")

// escapeUntrusted neutralises any attempt by page text to close or open our
// delimiter (or fake a task_memory block) by replacing the angle bracket.
func escapeUntrusted(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	for _, tag := range []string{"untrusted_page_content", "task_memory", "system", "user_message"} {
		s = strings.ReplaceAll(s, "</"+tag, "&lt;/"+tag)
		s = strings.ReplaceAll(s, "<"+tag, "&lt;"+tag)
	}
	return s
}

// wrapUntrusted wraps page-derived text for the model.
func wrapUntrusted(kind, tab, pageURL, body string) string {
	var sb strings.Builder
	sb.Grow(len(body) + 120)
	sb.WriteString(untrustedOpen)
	sb.WriteString(` kind="` + attrClean.Replace(kind) + `"`)
	if tab != "" {
		sb.WriteString(` tab="` + attrClean.Replace(tab) + `"`)
	}
	if pageURL != "" {
		u := pageURL
		if len(u) > 300 {
			u = u[:300] + "..."
		}
		sb.WriteString(` url="` + attrClean.Replace(u) + `"`)
	}
	sb.WriteString(">\n")
	sb.WriteString(escapeUntrusted(body))
	if !strings.HasSuffix(body, "\n") {
		sb.WriteByte('\n')
	}
	sb.WriteString(untrustedClose)
	return sb.String()
}

// ---- URL policy -------------------------------------------------------------

var internalSchemes = map[string]bool{
	"chrome": true, "sweef": true, "about": true, "devtools": true, "chrome-extension": true,
	"view-source": true, "file": true, "data": true, "javascript": true, "blob": true,
	"edge": true, "brave": true, "moz-extension": true, "ws": true, "wss": true, "ftp": true,
}

// isInternalURL reports pages the agent may never see or open.
func isInternalURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "about:blank" {
		return false
	}
	i := strings.Index(raw, ":")
	if i <= 0 {
		return false
	}
	scheme := strings.ToLower(raw[:i])
	return internalSchemes[scheme]
}

// normalizeNavURL adds https:// when no scheme is present.
func normalizeNavURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	if i := strings.Index(raw, "://"); i > 0 {
		return raw
	}
	if i := strings.Index(raw, ":"); i > 0 && !strings.Contains(raw[:i], ".") && !isPortSuffix(raw[i+1:]) {
		return raw // some other scheme (about:, javascript:, ...), let the denylist judge it
	}
	return "https://" + raw
}

func isPortSuffix(s string) bool {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n > 0 && (n == len(s) || s[n] == '/')
}

// checkNavigable applies the denylist to a navigation target.
func checkNavigable(raw string) error {
	u := normalizeNavURL(raw)
	if u == "about:blank" {
		return nil
	}
	if isInternalURL(u) {
		return fmt.Errorf("navigation to internal or non-web page %q is not allowed", clip(raw, 60))
	}
	pu, err := url.Parse(u)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") {
		return fmt.Errorf("%q is not a valid http(s) URL", clip(raw, 60))
	}
	if pu.Hostname() == "" {
		return fmt.Errorf("%q has no host", clip(raw, 60))
	}
	if pu.User != nil {
		return fmt.Errorf("URLs with embedded credentials are not allowed")
	}
	return nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// registrableDomain returns the eTLD+1 of a URL's host, or the host itself
// (IP addresses, localhost, unknown suffixes).
func registrableDomain(raw string) string {
	pu, err := url.Parse(normalizeNavURL(raw))
	if err != nil {
		return ""
	}
	h := strings.ToLower(pu.Hostname())
	if h == "" {
		return ""
	}
	if net.ParseIP(h) != nil { // an IP literal has no registrable domain
		return h
	}
	if d, err := publicsuffix.EffectiveTLDPlusOne(h); err == nil {
		return d
	}
	return h
}

// ---- page visibility ----------------------------------------------------------

// visibility records the pages the user marked not-allowed.
type visibility struct {
	mu    sync.RWMutex
	rules map[string]bool // normalized origin or host -> allowed
}

func newVisibility() *visibility { return &visibility{rules: map[string]bool{}} }

func ruleKey(origin string) string {
	origin = strings.ToLower(strings.TrimSpace(origin))
	origin = strings.TrimSuffix(origin, "/")
	return origin
}

// Set records the user's decision for an origin ("https://example.com") or a
// bare host ("example.com", covering every scheme and port).
func (v *visibility) Set(origin string, allowed bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if allowed {
		delete(v.rules, ruleKey(origin)) // default is allowed
		return
	}
	v.rules[ruleKey(origin)] = false
}

// Allowed reports whether the model may see the page at rawURL. Internal
// pages are never allowed.
func (v *visibility) Allowed(rawURL string) bool {
	if isInternalURL(rawURL) {
		return false
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.rules) == 0 {
		return true
	}
	pu, err := url.Parse(rawURL)
	if err != nil || pu.Host == "" {
		return true
	}
	origin := strings.ToLower(pu.Scheme + "://" + pu.Host)
	host := strings.ToLower(pu.Hostname())
	for _, k := range []string{origin, strings.ToLower(pu.Host), host} {
		if _, blocked := v.rules[k]; blocked {
			return false
		}
	}
	return true
}

const hiddenText = "[hidden by user]"

// ---- risk classification --------------------------------------------------------

// Risk is a reason to ask the user before running a step.
type Risk struct {
	Kind   ConfirmKind
	Reason string
}

var (
	rePayment = regexp.MustCompile(`(?i)\b(place (your |my )?order|pay( now| with| securely)?|buy( it)? now|purchase|complete (the )?(purchase|order|payment|booking)|confirm (the )?(order|payment|purchase|booking)|submit (the )?(order|payment)|subscribe|donate|start (a )?(free )?trial|book now|authori[sz]e payment|pay and)\b`)
	reMessage = regexp.MustCompile(`(?i)\b(send|post|publish|reply|tweet|share|submit (comment|review|message|reply)|add comment)\b`)
	reDelete  = regexp.MustCompile(`(?i)\b(delete|erase|destroy|deactivate|permanently|close (my |your )?account|cancel (my |your )?(subscription|membership|account|order)|unsubscribe|clear (all|history|data)|remove (my |your )?account|empty trash)\b`)
	reCreds   = regexp.MustCompile(`(?i)\b((change|update|reset|set|create|save|add) (a |your |new |the )?(password|passcode|passkey|email address|phone number|2fa|two.factor|security (question|key)|recovery)|api key|revoke)\b`)
	reCard    = regexp.MustCompile(`(?i)card|cvv|cvc|security code|iban|routing`)
	reSecret  = regexp.MustCompile(`(?i)password|passcode|\bcvv\b|\bcvc\b|security code|card number|credit card|debit card|\bssn\b|social security|routing number|iban`)
)

var actionRoles = map[string]bool{"button": true, "link": true, "menuitem": true, "menuitemcheckbox": true, "option": true, "tab": true, "switch": true, "": true}

// classifyStep decides from the step and what the latest snapshot says about
// its target whether the user must approve it. cur is the current page URL.
// The classifier is intentionally keyword based and conservative on verbs
// that cannot be undone; it is a safety net behind the system prompt, not a
// replacement for it.
func classifyStep(s Step, ri refInfo, known bool, cur string) *Risk {
	label := ri.Role + " " + ri.Name
	switch s.Op {
	case OpClick:
		if !known || !actionRoles[ri.Role] {
			return nil
		}
		name := ri.Name
		switch {
		case rePayment.MatchString(name):
			return &Risk{ConfirmPayment, "may submit a payment or order"}
		case reDelete.MatchString(name):
			return &Risk{ConfirmDelete, "may delete or cancel something"}
		case reCreds.MatchString(name):
			return &Risk{ConfirmCredentials, "may change account credentials"}
		case reMessage.MatchString(name):
			return &Risk{ConfirmMessage, "may send or publish a message"}
		}
	case OpType:
		if known && reSecret.MatchString(label+" "+ri.Extra) {
			k := ConfirmCredentials
			if reCard.MatchString(label) {
				k = ConfirmPayment
			}
			return &Risk{k, "types into a sensitive field (" + clip(strings.TrimSpace(label), 40) + ")"}
		}
		if s.Submit && known && reMessage.MatchString(ri.Name) {
			return &Risk{ConfirmMessage, "submits a message"}
		}
	case OpPress:
		if known && strings.EqualFold(s.Key, "Enter") && reMessage.MatchString(ri.Name) {
			return &Risk{ConfirmMessage, "submits a message"}
		}
	}
	return nil
}

// ---- per-run state ----------------------------------------------------------------

// runSafety holds the mutable safety state of one run.
type runSafety struct {
	mu          sync.Mutex
	maxActions  int
	maxNavs     int
	actions     int
	navs        int
	tainted     bool // untrusted page text has been shown to the model
	trusted     map[string]bool
	confirmedCD map[string]bool
}

func newRunSafety(maxActions, maxNavs int) *runSafety {
	return &runSafety{maxActions: maxActions, maxNavs: maxNavs, trusted: map[string]bool{}, confirmedCD: map[string]bool{}}
}

var domainTokenRe = regexp.MustCompile(`(?i)\b(?:https?://)?((?:[a-z0-9-]+\.)+[a-z]{2,})\b`)

// seedTrusted adds the registrable domains the user's own text mentions.
func (s *runSafety) seedTrusted(texts ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range texts {
		for _, m := range domainTokenRe.FindAllStringSubmatch(t, -1) {
			h := strings.ToLower(m[1])
			if d, err := publicsuffix.EffectiveTLDPlusOne(h); err == nil {
				s.trusted[d] = true
			}
		}
	}
}

func (s *runSafety) trust(rawURL string) {
	if d := registrableDomain(rawURL); d != "" {
		s.mu.Lock()
		s.trusted[d] = true
		s.mu.Unlock()
	}
}

func (s *runSafety) taint() {
	s.mu.Lock()
	s.tainted = true
	s.mu.Unlock()
}

func (s *runSafety) isTainted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tainted
}

// crossDomain reports whether moving from `from` to `to` needs approval:
// untrusted text has been read, the registrable domain changes, and the user
// neither mentioned nor approved the target domain.
func (s *runSafety) crossDomain(from, to string) (bool, string) {
	td := registrableDomain(to)
	if td == "" {
		return false, ""
	}
	fd := registrableDomain(from)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.tainted || td == fd || s.trusted[td] {
		return false, ""
	}
	return true, td
}

// charge counts steps and navigations against the run limits.
func (s *runSafety) charge(actions, navs int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.actions+actions > s.maxActions {
		return fmt.Errorf("action limit reached (%d steps per run); finish with what you have or ask the user to continue", s.maxActions)
	}
	if s.navs+navs > s.maxNavs {
		return fmt.Errorf("navigation limit reached (%d per run); finish with what you have", s.maxNavs)
	}
	s.actions += actions
	s.navs += navs
	return nil
}

func (s *runSafety) counts() (actions, navs int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.actions, s.navs
}
