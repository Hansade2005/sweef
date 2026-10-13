# Notifications

Code: `internal/engine/pwa*.go`, `platformjs/pwa.js` (the API), `internal/notify` (delivery), `internal/dbus` (D-Bus client),
`internal/app/notifications.go` (permissions and prompt), `internal/ui/web/chrome/perm.js` (prompt).

## API

`Notification` (constructor with `body`, `tag`, `renotify`, `silent`, `requireInteraction`, `icon`, `image`, `badge`,
`data`, `dir`, `lang`, `timestamp`; `vibrate` is accepted and ignored; `actions` throw in the constructor), `permission`,
`requestPermission()` (promise and callback forms), `maxActions` (2), `close()`, `onclick/onshow/onerror/onclose`.
`ServiceWorkerRegistration.showNotification()` (with `actions`), `getNotifications({tag})`, and in the worker
`notificationclick` / `notificationclose` (`NotificationEvent`: `notification`, `action`). `clients.openWindow()` opens a
tab, `WindowClient.focus()` focuses one, `WindowClient.navigate()` navigates a client the worker controls (same origin).
`openWindow` and `focus` need attention, as in Chromium: they work while a notification click is handled (and for ten
seconds after it starts) or when the client had a user gesture, otherwise they reject with `InvalidAccessError`. A
cross-origin `openWindow` URL opens the tab and resolves `null`. The Permissions API answers `notifications`, `push`,
`periodic-background-sync`, `background-sync` and reports changes through `onchange`. `navigator.setAppBadge` /
`clearAppBadge` store the badge per origin; the app exposes it (`App.Badges`, `state.perm.badges`); no shell draws it yet.
Replacement by `tag` keeps one notification (silent unless `renotify`). A page's notifications close when the page goes
away; at most 25 live per origin.

## Permission model

Per origin, stored in the profile's site settings (`sites.json`): allowed, blocked, or not decided (ask). The setting
`notifications.default` can be "Block without asking". Not available in private windows (permission is `denied`, no
prompt). Quiet UI: a request without a user gesture in the last 5 seconds is not shown and resolves `default` (nothing is
stored); three "Not now" answers in a row pause the prompt for the site for a week (requests resolve `denied`). The user
gesture comes from real input events delivered by the window shell, not from page scripts.

**Prompt**: a card under the site button: "{site} wants to Show notifications" with Allow, Block, Not now. It does not
take the focus; Alt+Shift+P (or Ctrl+L then Tab) reaches it, arrow keys move between the buttons, Escape is Not now.
**Site info** (padlock) has a Notifications entry to change the decision. **Settings > Notifications** lists decisions,
the default, Do not disturb and the push service. **Do not disturb** (main menu or settings, `notifications.dnd`) keeps
notifications off the screen; pages are told they were shown.

## Delivery

`notify.Sink` is the interface; `notify.Recorder` is the test sink; `notify.Fallback` shows an in-window toast when the
native sink is missing or fails. The native sink is opened when the first notification is shown.

| Platform | Backend | Status |
|---|---|---|
| Linux, BSD | `org.freedesktop.Notifications` over the session bus (`Notify` with actions, hints `urgency`, `suppress-sound`, `desktop-entry`; `CloseNotification`; `NotificationClosed`, `ActionInvoked`; replaces by tag) | Run against a fake daemon and a real `dbus-daemon`; not run against GNOME, KDE or a real notification daemon |
| Windows | `Shell_NotifyIconW` balloon notifications (toasts on Windows 10/11), one icon per notification, click and timeout events; no action buttons | **Compiled and vetted only (GOOS=windows); never run** |
| macOS | `NSUserNotificationCenter` through purego's Objective-C runtime, delegate for clicks; needs a bundled app | **Compiled and vetted only (GOOS=darwin); never run** |

`internal/dbus` is a minimal pure-Go D-Bus client (wire format, EXTERNAL auth, calls, signals, serving calls). It is
tested with hand-computed bytes, an in-memory peer, a real `dbus-daemon` and `dbus-send` as an independent client. Signals
are only believed from the owner of the notification service name.

Not done: notification sounds beyond `silent`, `image`/`badge` display (icons are fetched to a temp file and passed as
`image-path`), `showTrigger`, dedicated `Worker` and `SharedWorker` use of the API.
