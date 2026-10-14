package sitestore

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Large values are not stored in the ordered KV store: a 500 MB value in the
// memtable and in every compaction would make memory and write amplification
// depend on the largest thing a page ever stored. A value above BlobThreshold
// (IndexedDB values, Cache Storage bodies, OPFS files) is a file under
// <origin>/blobs/<id> and the store holds only its reference.
//
// Crash safety. A blob is written to a temporary name, synced and renamed
// before the batch that refers to it is committed, and the batch also writes a
// registry entry (pBlob|id -> size). When a record is replaced or deleted the
// same batch deletes the registry entry and the file is removed after the
// commit. A crash can therefore only leave files nobody refers to; opening the
// store lists the directory and removes every file without a registry entry
// (one point lookup per file, never a scan of the data).

// BlobThreshold is the size above which a value is kept outside the store.
const BlobThreshold = 64 << 10

var errNoBlob = errors.New("sitestore: blob missing")

func blobKey(id uint64) []byte {
	k := make([]byte, 9)
	k[0] = pBlob
	binary.BigEndian.PutUint64(k[1:], id)
	return k
}

func (o *Origin) blobPath(id uint64) string {
	return filepath.Join(o.dir, "blobs", strconv.FormatUint(id, 10))
}

// newBlob stores data as a blob and returns its id. The caller registers it in the batch that refers to it.
func (o *Origin) newBlob(data []byte) (uint64, error) {
	id := o.allocID()
	if o.dir == "" {
		o.memMu.Lock()
		if o.memBlobs == nil {
			o.memBlobs = map[uint64][]byte{}
		}
		o.memBlobs[id] = append([]byte(nil), data...)
		o.memMu.Unlock()
		return id, nil
	}
	if err := os.MkdirAll(filepath.Join(o.dir, "blobs"), 0o700); err != nil {
		return 0, err
	}
	p := o.blobPath(id)
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return id, nil
}

// readBlob reads a blob whole.
func (o *Origin) readBlob(id uint64) ([]byte, error) {
	if o.dir == "" {
		o.memMu.Lock()
		b, ok := o.memBlobs[id]
		o.memMu.Unlock()
		if !ok {
			return nil, errNoBlob
		}
		return b, nil
	}
	b, err := os.ReadFile(o.blobPath(id))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoBlob, err)
	}
	return b, nil
}

// openBlob opens a blob for streaming; the caller closes it.
func (o *Origin) openBlob(id uint64) (io.ReadCloser, int64, error) {
	if o.dir == "" {
		b, err := o.readBlob(id)
		if err != nil {
			return nil, 0, err
		}
		return io.NopCloser(strings.NewReader(string(b))), int64(len(b)), nil
	}
	f, err := os.Open(o.blobPath(id))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", errNoBlob, err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

// dropBlobFiles removes blob files (after the batch that unregistered them committed). A file that cannot be
// removed now (still open on Windows) is picked up by the sweep of the next open.
func (o *Origin) dropBlobFiles(ids []uint64) {
	if len(ids) == 0 {
		return
	}
	if o.dir == "" {
		o.memMu.Lock()
		for _, id := range ids {
			delete(o.memBlobs, id)
		}
		o.memMu.Unlock()
		return
	}
	for _, id := range ids {
		_ = os.Remove(o.blobPath(id))
	}
}

// sweepBlobs deletes the files in blobs/ that the store does not register.
func (o *Origin) sweepBlobs(k *kvStore) {
	if o.dir == "" {
		return
	}
	dir := filepath.Join(o.dir, "blobs")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		name := e.Name()
		id, err := strconv.ParseUint(name, 10, 64)
		if err != nil { // *.tmp and anything else
			_ = os.Remove(filepath.Join(dir, name))
			continue
		}
		if _, ok := k.get(blobKey(id)); !ok {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// ---- record values --------------------------------------------------------------------------------

// A stored value is [version tag][flags][payload]. The tag lets a later build change the layout and still read
// this one; flagBlob means the payload is [blob id u64][size u64] instead of the bytes.
const (
	valV1    = 1
	flagBlob = 1
)

// packValue builds the stored form of a value, externalising it when it is large. It returns the blob id (0 if
// inline). The cost is what the value counts against the quota.
func (o *Origin) packValue(payload []byte) (stored []byte, blob uint64, err error) {
	if len(payload) <= BlobThreshold {
		stored = make([]byte, 2+len(payload))
		stored[0], stored[1] = valV1, 0
		copy(stored[2:], payload)
		return stored, 0, nil
	}
	id, err := o.newBlob(payload)
	if err != nil {
		return nil, 0, err
	}
	stored = make([]byte, 2+16)
	stored[0], stored[1] = valV1, flagBlob
	binary.BigEndian.PutUint64(stored[2:], id)
	binary.BigEndian.PutUint64(stored[10:], uint64(len(payload)))
	return stored, id, nil
}

// valueInfo describes a stored value without reading a blob.
func valueInfo(stored []byte) (blob uint64, size int64, ok bool) {
	if len(stored) < 2 || stored[0] != valV1 {
		return 0, 0, false
	}
	if stored[1]&flagBlob != 0 {
		if len(stored) < 18 {
			return 0, 0, false
		}
		return binary.BigEndian.Uint64(stored[2:]), int64(binary.BigEndian.Uint64(stored[10:])), true
	}
	return 0, int64(len(stored) - 2), true
}

// unpackValue returns the payload of a stored value.
func (o *Origin) unpackValue(stored []byte) ([]byte, error) {
	if len(stored) < 2 || stored[0] != valV1 {
		return nil, errCorrupt
	}
	if stored[1]&flagBlob == 0 {
		return stored[2:], nil
	}
	id, _, ok := valueInfo(stored)
	if !ok {
		return nil, errCorrupt
	}
	return o.readBlob(id)
}
