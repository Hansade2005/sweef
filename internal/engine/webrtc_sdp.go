package engine

import "strings"

// sdpShape reduces a session description to what must not change when a page edits ("munges") the SDP that
// createOffer / createAnswer produced: the media sections in order with their kind, mid, ICE credentials,
// fingerprint, DTLS role, direction, simulcast and rid lines. Codec lines, fmtp parameters, bandwidth and
// vendor flags are left out; those are what pages typically rewrite.
func sdpShape(sdp string) string {
	var b strings.Builder
	for _, ln := range strings.Split(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(ln, "m="):
			if f := strings.Fields(ln); len(f) > 0 {
				b.WriteString(f[0]) // m=audio / m=video / m=application: the kind only (ports and payload types are free)
			}
		case strings.HasPrefix(ln, "a=mid:"), strings.HasPrefix(ln, "a=ice-ufrag:"), strings.HasPrefix(ln, "a=ice-pwd:"),
			strings.HasPrefix(ln, "a=fingerprint:"), strings.HasPrefix(ln, "a=setup:"), strings.HasPrefix(ln, "a=rid:"),
			strings.HasPrefix(ln, "a=simulcast:"), strings.HasPrefix(ln, "a=group:BUNDLE"), ln == "a=sendrecv", ln == "a=sendonly",
			ln == "a=recvonly", ln == "a=inactive":
			b.WriteString(strings.TrimSpace(ln))
		default:
			continue
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// sameSDPShape reports whether an edited description differs from the original only in lines that do not
// change what the connection negotiates structurally (see sdpShape). Candidate lines are not compared: the
// page sees the sanitised ones.
func sameSDPShape(edited, original string) bool {
	return sdpShape(edited) == sdpShape(original)
}
