package engine

// cookie_rt.go: the cookies of every request the engine makes for a page. cacheRT calls cookiesRoundTrip for
// each hop (the first request and every redirect), so cookies are attached and stored per hop, with the policy of
// partition.go: a request in a third-party context neither sends nor stores cookies in the unpartitioned jar, it
// uses the partition of the top-level site (or nothing, under CookiesBlocked); a request without credentials uses
// none. There is deliberately no net/http cookie jar behind the client: it could not tell the contexts apart.

import (
	"net/http"
	"strings"
)

func (c *cacheRT) RoundTrip(req *http.Request) (*http.Response, error) {
	mi, _ := req.Context().Value(meterKey).(*meterInfo)
	if mi == nil || mi.c == nil || mi.noCreds {
		return c.roundTrip(req)
	}
	sc := cookieScope{unpart: true}
	if mi.t != nil {
		sc = mi.t.cookieScopeFor(req.URL.String(), mi.kind)
	}
	jar := mi.c.jar
	// a caller (a route's continue(), a download) that set Cookie itself keeps it
	if req.Header.Get("Cookie") == "" {
		if h := jar.Header(req.URL.String(), sc.query(false)); h != "" {
			req = req.Clone(req.Context())
			req.Header.Set("Cookie", h)
		}
	}
	res, err := c.roundTrip(req)
	if err != nil || res == nil {
		return res, err
	}
	if mi.cacheState() == "hit" { // answered from the cache: no response came from the server, nothing to store
		return res, nil
	}
	host := req.URL.Hostname()
	for _, sch := range res.Header.Values("Set-Cookie") {
		if ck := parseCookie(sch, c.eng.now()); ck != nil {
			mi.c.storeCookie(ck, strings.ToLower(host), sc)
		}
	}
	return res, nil
}
