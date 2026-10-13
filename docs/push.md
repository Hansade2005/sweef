# Web Push and `sweef pushd`

Sweef implements the browser side of Web Push (RFC 8030 delivery, RFC 8291 `aes128gcm` encryption, RFC 8292 VAPID)
and ships a small push relay, `sweef pushd`, that anyone can host. No push service is built in: Google FCM and Apple
APNs need vendor accounts and Sweef does not use them. With no service configured, `pushManager.subscribe()` rejects
with `AbortError: Registration failed - push service not available`, as a browser without one does.

## Architecture

```
  application server                      push service (sweef pushd, or any autopush service)            Sweef
 ┌───────────────────┐  POST /wpush/v2/<token>   ┌─────────────────────────────────┐   WebSocket   ┌──────────────────────┐
 │ encrypts payload  │  TTL, Urgency, Topic,     │ endpoint = random id + HMAC     │  hello        │ autopush client      │
 │ (RFC 8291) and    │  Authorization: vapid ... │ stores encrypted bodies up to   │  register     │ (internal/webpush/   │
 │ signs VAPID       │ ────────────────────────► │ their TTL, bounded; forwards    │ ◄───────────► │  autopush)           │
 │ (RFC 8292)        │  201 Created + Location   │ while the browser is connected  │  notification │ decrypt, `push` event│
 └───────────────────┘                           └─────────────────────────────────┘  ack, ping    │ in the service worker│
                                                                                                    └──────────────────────┘
```

Code: `internal/webpush` (crypto, `Transport` interface), `internal/webpush/autopush` (default transport),
`internal/pushd` (relay), `internal/engine/pwa_push.go` (subscriptions, events, policy), `cmd/sweef/pushd.go`.

## Using it in the browser

The service address is, in this order: `PWAOptions.PushServiceFunc`/`PushService` (the app passes the setting
`push.service`, editable in Settings, Notifications), then the environment variable `SWEEF_PUSH_SERVICE`. `http(s)://`
means `ws(s)://`. Changing it while the browser runs drops the old subscriptions (each worker gets
`pushsubscriptionchange`). The transport sits behind `webpush.Transport`; `PWAOptions.PushTransport` replaces it.

Supported: `PushManager.subscribe({userVisibleOnly: true, applicationServerKey})`, `getSubscription`, `permissionState`,
`supportedContentEncodings` (`aes128gcm` only; the older `aesgcm` is not implemented), `PushSubscription`
(`endpoint`, `getKey`, `toJSON`, `unsubscribe`, `options`; `expirationTime` is always `null`), the `push` event with
`PushMessageData` (`text`, `json`, `arrayBuffer`, `blob`, `bytes`), `pushsubscriptionchange` (the browser resubscribes
with the same options when it can, and passes both subscriptions), `userVisibleOnly: false` is refused with
`NotSupportedError`. Subscriptions are bound to a service worker registration, stored per origin
(`<profile>/storage/<origin>/pwa.json`, private key included, mode 0600) and removed when the registration is
unregistered, the permission is revoked or site data is cleared. Push is unavailable in private windows.

**Silent push policy.** A push that does not show a notification within 5 seconds of its event finishing gets a generic
one ("This site has been updated in the background.", tag `user_visible_auto_notification`; the 5 seconds are our
choice, not a standard). Clicking it focuses a tab of the site.

**When pushes arrive.** Only while the Sweef process runs. The relay keeps messages up to their TTL and delivers them at
the next start (tested: a message posted while the browser was closed shows its notification after restart). A
background daemon or tray mode that keeps the process alive is not built.

## `sweef pushd`

```
sweef pushd --addr 127.0.0.1:8090 --public-url https://push.example.com --data /var/lib/sweef-pushd
```

Put TLS in front (reverse proxy; `--trust-forwarded` if it sets `X-Forwarded-For`). Browsers connect to
`wss://push.example.com/` (or `/ws`); give them `https://push.example.com` as the push service.
`GET /health` returns counters.

Application-server API (`POST /wpush/v2/<token>`): `TTL` required (`0` = deliver now or drop; clamped to `--max-ttl`,
reported back in the `TTL` response header); `Urgency` `very-low|low|normal|high`; `Topic` (at most 32 URL-safe base64
characters; a newer message replaces the waiting one); body empty or `Content-Encoding: aes128gcm`, at most 4096 bytes.
Answers: `201` + `Location` (a message resource; `DELETE` withdraws it before delivery), `400` bad headers, `401`/`403`
VAPID missing, invalid, for another audience, expired or more than 24 h ahead, or not the key the subscription was made
with, `404` unknown or forged token, `410` the subscription was removed (the browser unsubscribed or lost it), `413` too
large, `429` rate limited (per source address and per endpoint, `Retry-After`), `503` store full.

Browser protocol: autopush JSON text frames: `hello`, `register` (with the VAPID key to bind), `unregister`,
`notification`, `ack`, and `{}` as ping. Messages stay stored until acknowledged, so a message the browser did not
acknowledge is delivered again. Limits: 100 stored messages per browser (oldest dropped), 200 subscriptions per browser,
64 MiB stored bytes, idle WebSockets closed after 10 minutes, browsers not seen for 60 days forgotten. State is kept in
`--data` (`state.json`, `secret`, mode 0600) so endpoints and queued messages survive restarts; without `--data` it is
memory only and a restart invalidates every endpoint.

### Security and privacy

* An endpoint is `base64url(random 16-byte id || HMAC-SHA256(secret, id)[:16])`. It cannot be forged without the secret,
  and it reveals nothing about the browser (the relay maps the random id to the subscription internally).
* The relay stores and forwards `aes128gcm` bodies it cannot read. It sees: endpoint ids, message sizes, timing,
  VAPID public keys and the source addresses of both sides. It never sees payloads, subscription keys or page URLs.
  It does not log payloads.
* A subscription made with an `applicationServerKey` only accepts posts signed by that key (RFC 8292), so a leaked
  endpoint alone cannot be used to send messages to it. Without a key, anyone with the endpoint can post.
* The relay is a single process with in-memory indexes; it is not a replicated service. It does not implement
  autopush's broadcast channel, `aesgcm` encoding, or message receipts.

## Tests

`go test ./internal/webpush/... ./internal/pushd` (RFC 8291 Appendix A and RFC 8292 section 2.4 vectors, relay status
codes, offline delivery, TTL expiry, topics, rate limits, persistence, reconnect backoff) and
`go test -run Push ./internal/engine` (an HTTP page registers a worker, subscribes through the in-process relay, an
application server written with only the standard library encrypts and posts, the worker's `push` handler shows a
notification into the recording sink; restart delivery; unsubscribe and `410`; revoked permission; a replacement
transport; `pushsubscriptionchange`).
