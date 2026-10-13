# Architecture decision records

An ADR records a decision that is expensive to reverse: what we chose, what else we considered, and why. Write one when a
change affects a documented contract, adds a significant dependency, changes the licence or release policy, or picks between
two designs that reasonable people would argue about.

How: copy [0000-template.md](0000-template.md) to `NNNN-short-title.md` (next number), fill it in, open a PR titled
`docs(adr): ...`. Status moves `Proposed` → `Accepted` (merged) → possibly `Superseded by NNNN`. Do not rewrite an accepted
ADR; supersede it.

| # | Title | Status |
|---|---|---|
| [0001](0001-apache-2-license.md) | License the project under Apache-2.0 | Accepted |
| [0002](0002-ordered-kv-store-for-site-storage.md) | An ordered key-value store (goleveldb) under IndexedDB, Cache Storage and OPFS | Accepted |
