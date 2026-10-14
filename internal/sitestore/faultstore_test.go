package sitestore

import (
	"math/rand"
	"sort"
	"sync"

	"github.com/syndtr/goleveldb/leveldb/storage"
)

// lossyStorage is a fault-injecting file layer for the ordered store: it
// remembers, per file, how many bytes were written and how many of those were
// made durable by Sync, and crash() produces what a power loss would leave
// behind: every file keeps its synced bytes and a random part of the
// unsynced ones (a torn write), the rest is gone. The store must open the
// result, show each transaction whole or not at all, and never lose a
// transaction that was committed with a synchronous write.
type lossyStorage struct {
	storage.Storage // an in-memory storage underneath

	mu      sync.Mutex
	data    map[storage.FileDesc][]byte // our own copy: the in-memory storage refuses to open a file that is still being written
	written map[storage.FileDesc]int
	synced  map[storage.FileDesc]int
}

func newLossyStorage() *lossyStorage {
	return &lossyStorage{Storage: storage.NewMemStorage(), data: map[storage.FileDesc][]byte{}, written: map[storage.FileDesc]int{}, synced: map[storage.FileDesc]int{}}
}

type lossyWriter struct {
	storage.Writer
	s  *lossyStorage
	fd storage.FileDesc
}

func (w *lossyWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.s.mu.Lock()
	w.s.written[w.fd] += n
	w.s.data[w.fd] = append(w.s.data[w.fd], p[:n]...)
	w.s.mu.Unlock()
	return n, err
}

func (w *lossyWriter) Sync() error {
	err := w.Writer.Sync()
	w.s.mu.Lock()
	w.s.synced[w.fd] = w.s.written[w.fd]
	w.s.mu.Unlock()
	return err
}

func (s *lossyStorage) Create(fd storage.FileDesc) (storage.Writer, error) {
	w, err := s.Storage.Create(fd)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.written[fd], s.synced[fd] = 0, 0
	s.data[fd] = nil
	s.mu.Unlock()
	return &lossyWriter{Writer: w, s: s, fd: fd}, nil
}

func (s *lossyStorage) Remove(fd storage.FileDesc) error {
	s.mu.Lock()
	delete(s.written, fd)
	delete(s.synced, fd)
	delete(s.data, fd)
	s.mu.Unlock()
	return s.Storage.Remove(fd)
}

func (s *lossyStorage) Rename(from, to storage.FileDesc) error {
	s.mu.Lock()
	s.written[to], s.synced[to], s.data[to] = s.written[from], s.synced[from], s.data[from]
	delete(s.written, from)
	delete(s.synced, from)
	delete(s.data, from)
	s.mu.Unlock()
	return s.Storage.Rename(from, to)
}

// crash returns the storage a power loss at this instant leaves: synced bytes survive, and of the unsynced
// ones a random prefix does (rnd == nil keeps none of them).
func (s *lossyStorage) crash(rnd *rand.Rand) storage.Storage {
	out := storage.NewMemStorage()
	s.mu.Lock()
	defer s.mu.Unlock()
	fds := make([]storage.FileDesc, 0, len(s.data))
	for fd := range s.data {
		fds = append(fds, fd)
	}
	sort.Slice(fds, func(i, j int) bool { // a fixed order, so a seed always produces the same crash
		if fds[i].Type != fds[j].Type {
			return fds[i].Type < fds[j].Type
		}
		return fds[i].Num < fds[j].Num
	})
	for _, fd := range fds {
		b := s.data[fd]
		keep := s.synced[fd]
		if keep > len(b) {
			keep = len(b)
		}
		if rnd != nil && keep < len(b) {
			keep += rnd.Intn(len(b) - keep + 1)
		}
		w, err := out.Create(fd)
		if err != nil {
			continue
		}
		_, _ = w.Write(b[:keep])
		_ = w.Sync()
		_ = w.Close()
	}
	if fd, err := s.Storage.GetMeta(); err == nil {
		_ = out.SetMeta(fd)
	}
	return out
}
