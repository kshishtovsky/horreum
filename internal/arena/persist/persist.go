// persist.go — PersistentManager wraps an arena.Manager with a
// Write-Ahead Log (WAL), durability mode, index checkpointing, and a
// background msync ticker.
//
// Lifecycle:
//
//	pm, err := persist.New("/var/lib/horreum", 64<<20, persist.Options{...})
//	defer pm.Close()
//
//	idx := index.New(1024)
//	if err := pm.LoadIndex(idx); err != nil { ... }
//
//	h, view, err := pm.Put([]byte("k"), []byte("v"))    // durable (in durable mode)
//	val, err := pm.View(h)                             // zero-copy read
//	pm.Sync()                                           // fsync WAL + arena
//	pm.Checkpoint(idx)                                  // snapshot index, rotate WAL
//
// On restart, New() opens the arena file, calls LoadIndex to
// reconstruct the HashIndex from the on-disk checkpoint (if any),
// and replays the WAL for any records appended after the checkpoint.
package persist

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/horreum/horreum/internal/arena"
	"github.com/horreum/horreum/internal/index"
)

// PersistentManager is a durable cache built on top of arena.Manager.
// It exposes Put/Get/Delete/Sync/Checkpoint/Close and a background
// ticker that periodically fsyncs the arena file.
type PersistentManager struct {
	mgr        *arena.Manager
	wal        *WAL
	dir        string
	regionSize uint64
	durable    bool
	mu         sync.Mutex // serialises WAL appends
	ticker     *time.Ticker
	stop       chan struct{}
	closed     atomic.Bool
}

// Options configures a PersistentManager.
type Options struct {
	// RegionSize is the arena region size in bytes.
	RegionSize uint64
	// Durable, when true, fsyncs the WAL after every Put/Delete
	// before returning success.  When false, the WAL is fsync'd
	// opportunistically by the background ticker.
	Durable bool
	// SyncInterval is the period of the background msync ticker.
	// Zero disables background syncing (caller is responsible for
	// explicit Sync calls).
	SyncInterval time.Duration
}

// New creates (or cold-starts) a PersistentManager rooted at dir.
//
// When dir/arena.dat does not exist, New creates the file and WAL,
// writes a fresh superblock, and returns a manager with an empty
// cache.  When the file exists, New opens it and validates the
// superblock; the caller should then invoke LoadIndex to rebuild the
// in-memory HashIndex.
func New(dir string, opts Options) (*PersistentManager, error) {
	if opts.RegionSize == 0 {
		return nil, errors.New("persist: RegionSize must be > 0")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("persist: mkdir %s: %w", dir, err)
	}

	mgr, err := ColdStart(dir, opts.RegionSize)
	if err != nil {
		return nil, err
	}

	walPath := filepath.Join(dir, "wal.log")
	walMax := walMaxLen(opts.RegionSize)
	wal, err := OpenWAL(walPath, int64(walMax))
	if err != nil {
		_ = mgr.Close()
		return nil, err
	}

	pm := &PersistentManager{
		mgr:        mgr,
		wal:        wal,
		dir:        dir,
		regionSize: opts.RegionSize,
		durable:    opts.Durable,
		stop:       make(chan struct{}),
	}
	if opts.SyncInterval > 0 {
		pm.ticker = time.NewTicker(opts.SyncInterval)
		go pm.runTicker()
	}
	return pm, nil
}

// Manager returns the underlying arena.Manager.  Use this to call
// IncFreq / GetFreq / Touch etc.
func (pm *PersistentManager) Manager() *arena.Manager { return pm.mgr }

// WAL exposes the underlying WAL for tests/benchmarks.  Production
// callers should use Put/Delete/Sync.
func (pm *PersistentManager) WAL() *WAL { return pm.wal }

// LoadIndex populates idx from the on-disk checkpoint, then replays
// the WAL to apply any post-checkpoint mutations.
//
// If no checkpoint exists, LoadIndex returns ErrCheckpointBadMagic
// and the caller should treat idx as empty (the WAL has already
// been validated by ColdStart).
func (pm *PersistentManager) LoadIndex(idx *index.HashIndex) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	entries, err := loadCheckpoint(pm.mgr, pm.regionSize)
	if err != nil {
		if errors.Is(err, ErrCheckpointBadMagic) {
			// No checkpoint yet — start from empty index, but
			// still apply WAL.
			if err := ReplayTo(pm.mgr, pm.wal, idx); err != nil {
				return err
			}
			pm.syncLiveObjects(idx)
			return nil
		}
		return err
	}
	for _, se := range entries {
		idx.Add(se.Key, se.Handle, se.ExpiresAt)
	}
	// Replay WAL on top of checkpoint.
	if err := ReplayTo(pm.mgr, pm.wal, idx); err != nil {
		return err
	}
	pm.syncLiveObjects(idx)
	return nil
}

// syncLiveObjects refreshes the underlying arena Manager's live-object
// counter from the index.  arena.Manager.liveObjs is process-local
// and is not persisted across restarts; the index is the only source
// of truth for "how many live handles exist" after a cold start.
// We also recompute UsedBytes as the sum of live handle sizes so
// Stats.UsedBytes matches the pre-restart value.
func (pm *PersistentManager) syncLiveObjects(idx *index.HashIndex) {
	pm.mgr.SetLiveObjects(uint64(idx.Count()))
	var used uint64
	for _, se := range idx.Snapshot() {
		used += uint64(se.Handle.Size)
	}
	pm.mgr.SetUsedBytes(used)
}

// Replay walks the WAL from offset 0 and applies every record to
// the supplied index.  See ReplayTo in recovery.go for details.
func (pm *PersistentManager) Replay(idx IndexWriter) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return ReplayTo(pm.mgr, pm.wal, idx)
}

// Dir returns the directory containing the arena and WAL files.
func (pm *PersistentManager) Dir() string { return pm.dir }

// Put stores value under key, appending a WAL record (and fsync'ing
// the WAL if durable mode is enabled).
//
// Put allocates an arena handle for value, writes value into the
// arena, and appends a SET record to the WAL (carrying the handle so
// recovery can re-establish the index without re-allocating in the
// arena).  In durable mode the WAL is fsync'd before returning
// success — guaranteeing that a crash immediately after a successful
// Put leaves the SET visible after recovery.
func (pm *PersistentManager) Put(key, value []byte) (arena.Handle, []byte, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.closed.Load() {
		return arena.Handle{}, nil, errors.New("persist: manager closed")
	}
	h, err := pm.mgr.Put(value)
	if err != nil {
		return arena.Handle{}, nil, err
	}
	if _, err := pm.wal.AppendSet(key, value, arenaHandleLike{Offset: h.Offset, Size: h.Size, Region: h.Region}); err != nil {
		_ = pm.mgr.Free(h)
		return arena.Handle{}, nil, err
	}
	if pm.durable {
		if err := pm.wal.Sync(); err != nil {
			_ = pm.mgr.Free(h)
			return arena.Handle{}, nil, fmt.Errorf("persist: wal sync: %w", err)
		}
	}
	view, err := pm.mgr.View(h)
	if err != nil {
		return arena.Handle{}, nil, err
	}
	return h, view, nil
}

// PutEx stores value under key with an expiration time, appending a WAL record.
func (pm *PersistentManager) PutEx(key, value []byte, expiresAt uint32) (arena.Handle, []byte, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.closed.Load() {
		return arena.Handle{}, nil, errors.New("persist: manager closed")
	}
	h, err := pm.mgr.Put(value)
	if err != nil {
		return arena.Handle{}, nil, err
	}
	if _, err := pm.wal.AppendSetEx(key, value, arenaHandleLike{Offset: h.Offset, Size: h.Size, Region: h.Region}, expiresAt); err != nil {
		_ = pm.mgr.Free(h)
		return arena.Handle{}, nil, err
	}
	if pm.durable {
		if err := pm.wal.Sync(); err != nil {
			_ = pm.mgr.Free(h)
			return arena.Handle{}, nil, fmt.Errorf("persist: wal sync: %w", err)
		}
	}
	view, err := pm.mgr.View(h)
	if err != nil {
		return arena.Handle{}, nil, err
	}
	return h, view, nil
}

// View returns the zero-copy view of the value at handle h.  This is
// identical to arena.Manager.View; it does not allocate.
func (pm *PersistentManager) View(h arena.Handle) ([]byte, error) {
	return pm.mgr.View(h)
}

// Delete removes key from the cache.  In durable mode the WAL is
// fsync'd before returning.
func (pm *PersistentManager) Delete(key []byte) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.closed.Load() {
		return errors.New("persist: manager closed")
	}
	if _, err := pm.wal.AppendDel(key); err != nil {
		return err
	}
	if pm.durable {
		return pm.wal.Sync()
	}
	return nil
}

// Sync flushes both the WAL and the arena's dirty pages to disk.
// Returns the first error encountered.
func (pm *PersistentManager) Sync() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if err := pm.wal.Sync(); err != nil {
		return err
	}
	return pm.mgr.Sync()
}

// SyncAsync schedules arena dirty pages to be flushed; does not block.
// WAL is still fsync'd synchronously (otherwise crash could lose
// records that the arena hasn't seen yet).
func (pm *PersistentManager) SyncAsync() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if err := pm.wal.Sync(); err != nil {
		return err
	}
	return pm.mgr.SyncAsync()
}

// Checkpoint writes the current state of idx into the arena file's
// checkpoint region, syncs, then truncates the WAL.  After a
// successful Checkpoint, the WAL is empty and the superblock
// CheckpointCount field equals idx.Count().
func (pm *PersistentManager) Checkpoint(idx *index.HashIndex) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	snap := idx.Snapshot()
	if _, err := writeCheckpoint(pm.mgr, snap, pm.regionSize); err != nil {
		return err
	}
	// Rotate the WAL — the checkpoint supersedes everything before it.
	if err := pm.wal.Rotate(); err != nil {
		return fmt.Errorf("persist: rotate wal after checkpoint: %w", err)
	}
	return nil
}

// Close stops the background ticker, closes the WAL, and closes the
// arena Manager.  Outstanding writes are NOT flushed automatically;
// call Sync first if durability matters.
func (pm *PersistentManager) Close() error {
	if !pm.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(pm.stop)
	if pm.ticker != nil {
		pm.ticker.Stop()
	}
	var firstErr error
	if err := pm.wal.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := pm.mgr.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// runTicker fires SyncAsync on each tick.
func (pm *PersistentManager) runTicker() {
	for {
		select {
		case <-pm.stop:
			return
		case <-pm.ticker.C:
			_ = pm.SyncAsync()
		}
	}
}
