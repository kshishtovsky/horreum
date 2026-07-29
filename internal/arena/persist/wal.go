// wal.go — append-only Write-Ahead Log.
//
// Each WAL record has a fixed-size header followed by the key/value
// payload.  The header size depends on the operation:
//
// SET record (header = 20 bytes):
//
//	┌──────────┬──────────┬──────────┬──────────┬──────────┬──────────┬──────────┬──────────┬──────────┐
//	│ magic(4) │ op(1)    │ keyLen(2)│ valLen(4)│ hOff(4)  │ hSize(4) │ hReg(1)  │ key      │ value    │
//	└──────────┴──────────┴──────────┴──────────┴──────────┴──────────┴──────────┴──────────┴──────────┘
//
// DEL record (header = 12 bytes):
//
//	┌──────────┬──────────┬──────────┬──────────┬──────────┬──────────┐
//	│ magic(4) │ op(1)    │ keyLen(2)│ valLen(4)│ key      │          │
//	└──────────┴──────────┴──────────┴──────────┴──────────┴──────────┘
//
//	magic = 0x57414C00  ("WAL\0")
//	op    = 1 (SET) | 2 (DEL)
//
// The log lives in its own file (wal.log).  It is append-only; reads
// during recovery scan from offset 0 to the current tail.
//
// v1.1: SET records now carry the original arena Handle (Offset/Size/
// Region) so recovery can re-establish the index entry WITHOUT copying
// the value into the arena a second time.  This eliminates the
// "orphan handle" bug where every replay allocated a fresh arena slot
// for the same payload.
package persist

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/horreum/horreum/internal/logger"
)

// WAL record op codes.
const (
	walOpSet uint8 = 1
	walOpDel uint8 = 2
)

// Header sizes per op.  Kept distinct so the tail repair code can
// detect corruption unambiguously.
const (
	walHeaderSizeSet = 4 + 1 + 2 + 4 + 4 + 4 + 1 // 20 bytes
	walHeaderSizeDel = 4 + 1 + 2 + 4             // 12 bytes
)

// TestWALHeaderSizeSet exposes the SET header constant for tests.
const TestWALHeaderSizeSet = walHeaderSizeSet

// TestWALHeaderSizeDel exposes the DEL header constant for tests.
const TestWALHeaderSizeDel = walHeaderSizeDel

// TestWALHeaderSize exposes the SET header size for backwards-compat
// with existing tests (used as "any-record header" lower bound).
const TestWALHeaderSize = walHeaderSizeSet

// File returns the underlying os.File.  Test-only helper; production
// callers must use the Append*/Iterate/Sync API instead.
func (w *WAL) File() *os.File { return w.file }

// WAL magic.
var walMagic = [4]byte{'W', 'A', 'L', 0}

// WAL is the on-disk log.  It owns the file descriptor and the
// monotonic write offset.  WAL is safe for concurrent use; the
// underlying file write is serialised via the embedded mutex.
type WAL struct {
	mu     sync.Mutex
	file   *os.File
	off    int64 // current write offset (also = file size after every sync)
	maxOff int64
	path   string
}

// walHeaderSize returns the on-disk header size for op.
func walHeaderSize(op uint8) int64 {
	switch op {
	case walOpSet:
		return walHeaderSizeSet
	case walOpDel:
		return walHeaderSizeDel
	default:
		return -1
	}
}

// OpenWAL opens (and creates if necessary) the WAL file at path with
// the given maximum length.  On open, any existing records past the
// last complete header are truncated (recovery treats them as
// garbage).
func OpenWAL(path string, maxLen int64) (*WAL, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("wal: open %s: %w", path, err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("wal: stat %s: %w", path, err)
	}
	off := st.Size()
	w := &WAL{file: f, off: off, maxOff: maxLen, path: path}
	if err := w.repairTail(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return w, nil
}

// Path returns the WAL file path.
func (w *WAL) Path() string { return w.path }

// Offset returns the current write offset (= file size after every
// successful write).
func (w *WAL) Offset() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.off
}

// repairTail scans the tail of the WAL file and truncates any
// incomplete record at the end.  This handles the case where the
// process crashed mid-write.
func (w *WAL) repairTail() error {
	if w.off == 0 {
		return nil
	}
	data := make([]byte, w.off)
	n, err := w.file.ReadAt(data, 0)
	if err != nil && err != io.EOF {
		return fmt.Errorf("wal: read: %w", err)
	}
	data = data[:n]
	// Walk records; the last complete one determines the valid size.
	pos := int64(0)
	for pos < int64(len(data)) {
		rem := int64(len(data)) - pos
		// Need at least the smallest header to read the op byte.
		if rem < walHeaderSizeDel {
			break
		}
		op := data[pos+4]
		hdrSize := walHeaderSize(op)
		if hdrSize < 0 || rem < hdrSize {
			break
		}
		keyLen := int64(binary.LittleEndian.Uint16(data[pos+5 : pos+7]))
		valLen := int64(binary.LittleEndian.Uint32(data[pos+7 : pos+11]))
		recLen := hdrSize + keyLen + valLen
		if rem < recLen {
			break
		}
		pos += recLen
	}
	if pos < int64(len(data)) {
		if err := w.file.Truncate(pos); err != nil {
			return fmt.Errorf("wal: truncate: %w", err)
		}
		if _, err := w.file.Seek(pos, io.SeekStart); err != nil {
			return fmt.Errorf("wal: seek: %w", err)
		}
		w.off = pos
	}
	return nil
}

// AppendSet appends a SET record and returns its on-disk offset.
//
// The supplied handle h is the original arena Handle assigned by
// arena.Manager.Put; recovery uses h to re-establish the index entry
// without re-allocating in the arena.
func (w *WAL) AppendSet(key, value []byte, h arenaHandleLike) (int64, error) {
	return w.appendRecord(walOpSet, key, value, h)
}

// arenaHandleLike is the subset of arena.Handle fields the WAL needs.
// Defined as a small struct so callers can pass an arena.Handle value
// without depending on the arena package's layout.
type arenaHandleLike struct {
	Offset uint32
	Size   uint32
	Region uint8
}

// ArenaHandleLikeForTest is a test helper for constructing
// arenaHandleLike values without importing arena.Handle directly.
func ArenaHandleLikeForTest(offset, size uint32, region uint8) arenaHandleLike {
	return arenaHandleLike{Offset: offset, Size: size, Region: region}
}

// AppendDel appends a DEL record.
func (w *WAL) AppendDel(key []byte) (int64, error) {
	return w.appendRecord(walOpDel, key, nil, arenaHandleLike{})
}

func (w *WAL) appendRecord(op uint8, key, value []byte, h arenaHandleLike) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	hdrSize := walHeaderSize(op)
	if hdrSize < 0 {
		return 0, fmt.Errorf("wal: unknown op %d", op)
	}
	recLen := hdrSize + int64(len(key)) + int64(len(value))
	if w.off+recLen > w.maxOff {
		return 0, errors.New("wal: log full; run checkpoint")
	}
	// Build the header in a stack buffer; payload via WriteAt.
	hdr := make([]byte, hdrSize)
	copy(hdr[0:4], walMagic[:])
	hdr[4] = op
	binary.LittleEndian.PutUint16(hdr[5:7], uint16(len(key)))
	if op == walOpSet {
		binary.LittleEndian.PutUint32(hdr[7:11], uint32(len(value)))
		binary.LittleEndian.PutUint32(hdr[11:15], h.Offset)
		binary.LittleEndian.PutUint32(hdr[15:19], h.Size)
		hdr[19] = h.Region
	} else {
		binary.LittleEndian.PutUint32(hdr[7:11], 0) // valLen=0
	}

	// Write header + payload via WriteAt for atomicity.
	if _, err := w.file.WriteAt(hdr, w.off); err != nil {
		return 0, fmt.Errorf("wal: write hdr: %w", err)
	}
	pos := w.off + hdrSize
	if len(key) > 0 {
		if _, err := w.file.WriteAt(key, pos); err != nil {
			return 0, fmt.Errorf("wal: write key: %w", err)
		}
		pos += int64(len(key))
	}
	if op == walOpSet && len(value) > 0 {
		if _, err := w.file.WriteAt(value, pos); err != nil {
			return 0, fmt.Errorf("wal: write val: %w", err)
		}
		pos += int64(len(value))
	}
	w.off += recLen
	return w.off - recLen, nil
}

// Sync flushes WAL data to disk.  Returns nil on success.
func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	t0 := time.Now()
	err := w.file.Sync()
	elapsed := time.Since(t0)
	if elapsed > logger.SlowLogThreshold {
		slog.Warn("slow WAL sync", "duration", elapsed, "path", w.path)
	}
	return err
}

// Close closes the WAL file.  Outstanding writes are NOT flushed; call
// Sync first if durability matters.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}

// Rotate truncates the WAL after a successful checkpoint.  Caller
// must ensure no concurrent reads.
func (w *WAL) Rotate() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.file.Truncate(0); err != nil {
		return err
	}
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	w.off = 0
	return nil
}

// WALRecord is one decoded record.
//
// Offset/Size/Region are only populated for SET records (Op == walOpSet).
// For DEL records they are zero.
type WALRecord struct {
	Op     uint8
	Key    []byte
	Value  []byte
	Offset uint32
	Size   uint32
	Region uint8
}

// WALIterator scans a WAL file from offset 0.
type WALIterator struct {
	file *os.File
	off  int64
	end  int64
}

// Iterate returns an iterator over the records in the WAL file.
// Use walOff=0 to scan from the beginning; otherwise pass the offset
// returned by AppendSet/AppendDel to resume from a known point.
func (w *WAL) Iterate(from int64) (*WALIterator, error) {
	w.mu.Lock()
	end := w.off
	w.mu.Unlock()
	return &WALIterator{file: w.file, off: from, end: end}, nil
}

// Next returns the next record, or io.EOF when exhausted.
func (it *WALIterator) Next() (WALRecord, error) {
	if it.off >= it.end {
		return WALRecord{}, io.EOF
	}
	// Read the minimum header first to learn the op.
	hdr := make([]byte, walHeaderSizeDel)
	n, err := it.file.ReadAt(hdr, it.off)
	if err != nil || n < walHeaderSizeDel {
		return WALRecord{}, io.EOF
	}
	if [4]byte{hdr[0], hdr[1], hdr[2], hdr[3]} != walMagic {
		return WALRecord{}, errors.New("wal: bad magic")
	}
	op := hdr[4]
	hdrSize := walHeaderSize(op)
	if hdrSize < 0 {
		return WALRecord{}, fmt.Errorf("wal: unknown op %d", op)
	}
	if hdrSize > walHeaderSizeDel {
		// Read the remainder of the SET header.
		extra := make([]byte, hdrSize-walHeaderSizeDel)
		if _, err := it.file.ReadAt(extra, it.off+int64(walHeaderSizeDel)); err != nil {
			return WALRecord{}, err
		}
		hdr = append(hdr, extra...)
	}
	keyLen := int(binary.LittleEndian.Uint16(hdr[5:7]))
	valLen := int(binary.LittleEndian.Uint32(hdr[7:11]))
	recLen := hdrSize + int64(keyLen) + int64(valLen)
	if it.off+recLen > it.end {
		return WALRecord{}, io.EOF
	}
	rec := WALRecord{Op: op}
	if op == walOpSet {
		rec.Offset = binary.LittleEndian.Uint32(hdr[11:15])
		rec.Size = binary.LittleEndian.Uint32(hdr[15:19])
		rec.Region = hdr[19]
	}
	if keyLen > 0 {
		rec.Key = make([]byte, keyLen)
		if _, err := it.file.ReadAt(rec.Key, it.off+int64(hdrSize)); err != nil {
			return WALRecord{}, err
		}
	}
	if op == walOpSet && valLen > 0 {
		rec.Value = make([]byte, valLen)
		if _, err := it.file.ReadAt(rec.Value, it.off+int64(hdrSize)+int64(keyLen)); err != nil {
			return WALRecord{}, err
		}
	}
	it.off += recLen
	return rec, nil
}
