# Context menus

The menu is a component of the chrome page (`internal/ui/web/chrome/ctx.js`, `ctx.css`), drawn in `#layer`, so it is composited
above the page and can extend outside the viewport. It loads on the first right click.

## Protocol

| Direction | Message | Meaning |
|---|---|---|
| Go to chrome | `ctx.menu {token,x,y,kind,items}` | show a menu built in Go (`internal/app/ctxmenu.go`) |
| Go to chrome | `ctx.close` | close it (scroll, resize, blur, tab change) |
| chrome to Go | `ctx.pick {token,id}` | an item was chosen |
| chrome to Go | `ctx.state {open,token}` | a menu opened or closed (focus is restored when it closes) |
| chrome to Go | `ctx.rightclick {x,y}` | right click on the scrim: open the menu of what is under it |
| page to Go | `ctx.show {items}` | an internal page's own menu; the choice returns to it as `ctx.pick {id}` |
| any to Go | `edit.op {op}` | undo, redo, cut, copy, paste, delete, selectall on the focused document |

Items are `{id,label,kbd,sep,disabled,danger,checked,sub}`. Internal pages call `SW.ctxMenu(event, [{label, on, ...}])`
(`shared/dom.js`, loaded lazily from `shared/ctxpage.js`); inputs without a menu of their own get the edit menu.

## Web pages

The contextmenu event is dispatched to the page first (right `clientX`/`clientY` and target); `preventDefault()` suppresses the menu.
The helper `__sweefPtr('context')` reports link, image, selection, editable state (with undo/redo availability) and media.
Menus: link, image, media, selection, editable, plain page (Back, Forward, Reload, Save page as, Print, View page source), and
`Inspect` on all of them.

## Hooks

- `a.copySelection(e)`, `cutSelection`, `pasteClipboard`, `selectAll`: thin calls into `clipboardOp` (`input.go`) for the tab the
  menu was opened for; the selection work lives in the engine.

## Limits

Native menus are not used. "Open link in new window" is not offered (there is one window). Copy image puts PNG on the in-app
clipboard and the image address on the system clipboard (the window shell carries text only).
