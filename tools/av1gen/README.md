# AV1 table generators

These scripts produced `internal/media/av1/tables_gen.go` and related generated files.
They parse the text of the public AV1 Bitstream & Decoding Process Specification
(AOMedia, 2019-01-08) and cross-check the default CDFs against libaom's read-only data
(`cross_check_cdfs.py`). The specification text itself is not stored in this repository:
download the PDF from https://aomediacodec.github.io/av1-spec/ , convert it to text, and
point `spectables.py` at it. The generated Go files carry their own headers.

libdav1d and libaom (both BSD-2-Clause) are credited in `NOTICE`.
