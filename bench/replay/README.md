# Replay proxy

`bench/replay` records real pages with all their subresources once and serves them again from localhost, so
page-load numbers can be compared without the noise of the live network. It is an HTTP proxy that intercepts
TLS with its own CA (written to `<dir>/ca.pem`).

```sh
go build -o /tmp/replay ./bench/replay
/tmp/replay -dir /tmp/replay-data -record -addr 127.0.0.1:8898 &   # forward to the network and store
# load each page once through it with Sweef (and Chromium), letting it run a few seconds, then:
kill %1; /tmp/replay -dir /tmp/replay-data -addr 127.0.0.1:8898 &   # answer from the store only
cat /tmp/replay-data/ca.pem /etc/ssl/certs/ca-certificates.crt > /tmp/replay-bundle.pem
HTTPS_PROXY=http://127.0.0.1:8898 SSL_CERT_FILE=/tmp/replay-bundle.pem sweef open https://github.com/golang/go
# Chromium: --proxy-server=http://127.0.0.1:8898 --ignore-certificate-errors --proxy-bypass-list='<-loopback>'
```

A request the store lacks is matched by method and URL, then by method and URL without the query; anything
else is answered 404 (`/__replay/stats` lists hits and misses). Recordings are not committed: they are third
parties' content (pages, scripts, images) and tens of megabytes. `-rtt` adds a fixed delay per answer.
