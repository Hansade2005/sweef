# Changelog

## [Unreleased]

### Added

- IndexedDB, `navigator.storage` and Cache Storage, persisted per origin under the profile's `storage` directory (append-only logs, response bodies on disk, quota, wiped by "Clear browsing data", memory-only in private windows); see docs/storage.md.
- Service workers: `navigator.serviceWorker`, the install/activate/fetch/message lifecycle, real interception of navigations, `fetch()`, XHR and sub-resource loads, registrations that survive a restart; see docs/service-workers.md.
