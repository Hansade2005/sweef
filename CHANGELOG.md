# Changelog

All notable changes to Sweef are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- WebAssembly JavaScript API on wazero (pure Go): `Module`, `Instance`, `Memory` (an ArrayBuffer over the live linear memory), `Table`, `Global`, `validate`/`compile`/`instantiate` and the streaming variants, in pages and Web Workers; no threads, `Tag` or `externref` ([docs/webassembly.md](docs/webassembly.md)).
