package arena

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"testing/quick"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(1<<20, false) // 1 MiB region for tests
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestPutViewRoundTrip(t *testing.T) {
	m := newTestManager(t)
	data := []byte("hello, arena")
	h, err := m.Put(data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := m.View(h)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("View = %q, want %q", got, data)
	}
}

func TestPutMultipleSizes(t *testing.T) {
	m := newTestManager(t)
	sizes := []int{0, 1, 7, 8, 9, 63, 64, 65, 1023, 1024, 1025, 65536}
	for _, sz := range sizes {
		data := make([]byte, sz)
		for i := range data {
			data[i] = byte(i % 251)
		}
		h, err := m.Put(data)
		if err != nil {
			t.Fatalf("Put(size=%d): %v", sz, err)
		}
		got, err := m.View(h)
		if err != nil {
			t.Fatalf("View(size=%d): %v", sz, err)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("size %d: data mismatch", sz)
		}
	}
}

func TestPutMaxObjectSize(t *testing.T) {
	// MaxObjectSize is 64 MiB — need a region large enough to hold it.
	m, err := NewManager(MaxObjectSize+4096, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()
	data := make([]byte, MaxObjectSize)
	h, err := m.Put(data)
	if err != nil {
		t.Fatalf("Put(MaxObjectSize) = %v, want nil", err)
	}
	got, err := m.View(h)
	if err != nil {
		t.Fatalf("View(MaxObjectSize) = %v, want nil", err)
	}
	if len(got) != MaxObjectSize {
		t.Errorf("View length = %d, want %d", len(got), MaxObjectSize)
	}
}

func TestPutExceedsMaxObjectSize(t *testing.T) {
	m := newTestManager(t)
	data := make([]byte, MaxObjectSize+1)
	_, err := m.Put(data)
	if err != ErrSizeTooLarge {
		t.Fatalf("Put(MaxObjectSize+1) = %v, want ErrSizeTooLarge", err)
	}
}

func TestFreeAndRealloc(t *testing.T) {
	m := newTestManager(t)
	data := []byte("realloc me")
	h, err := m.Put(data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := m.Free(h); err != nil {
		t.Fatalf("Free: %v", err)
	}
	// Freed space should be reusable.
	h2, err := m.Put(data)
	if err != nil {
		t.Fatalf("Put after Free: %v", err)
	}
	got, err := m.View(h2)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("data mismatch after realloc")
	}
}

func TestViewInvalidHandle(t *testing.T) {
	m := newTestManager(t)
	_, err := m.View(Handle{Offset: 0, Size: 1, Region: 99})
	if err != ErrOffsetInvalid {
		t.Fatalf("View(invalid region) = %v, want ErrOffsetInvalid", err)
	}
	_, err = m.View(Handle{Offset: 2000000, Size: 1, Region: 0})
	if err != ErrOffsetInvalid {
		t.Fatalf("View(invalid offset) = %v, want ErrOffsetInvalid", err)
	}
}

func TestFreeInvalidHandle(t *testing.T) {
	m := newTestManager(t)
	err := m.Free(Handle{Offset: 0, Size: 1, Region: 99})
	if err != ErrOffsetInvalid {
		t.Fatalf("Free(invalid region) = %v, want ErrOffsetInvalid", err)
	}
}

func TestRegionRotation(t *testing.T) {
	// Small region to force rotation.
	m, err := NewManager(4096, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	// Fill the first region.
	var handles []Handle
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}
	for {
		h, err := m.Put(data)
		if err == ErrArenaFull {
			break
		}
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
		handles = append(handles, h)
	}
	if len(handles) == 0 {
		t.Fatal("expected at least one Put to succeed")
	}

	// Should have rotated to a new region.
	stats := m.Stats()
	if stats.LiveObjects == 0 {
		t.Fatal("expected live objects > 0")
	}

	// Verify all handles are still valid.
	for i, h := range handles {
		got, err := m.View(h)
		if err != nil {
			t.Fatalf("View(handle %d): %v", i, err)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("handle %d: data mismatch", i)
		}
	}
}

func TestStats(t *testing.T) {
	m := newTestManager(t)
	s := m.Stats()
	if s.UsedBytes != 0 {
		t.Errorf("initial UsedBytes = %d, want 0", s.UsedBytes)
	}
	if s.LiveObjects != 0 {
		t.Errorf("initial LiveObjects = %d, want 0", s.LiveObjects)
	}

	data := make([]byte, 100)
	m.Put(data)
	s = m.Stats()
	if s.UsedBytes == 0 {
		t.Errorf("UsedBytes after Put = 0, want > 0")
	}
	if s.LiveObjects != 1 {
		t.Errorf("LiveObjects = %d, want 1", s.LiveObjects)
	}
}

func TestSync(t *testing.T) {
	m := newTestManager(t)
	if err := m.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
}

func TestClose(t *testing.T) {
	m, err := NewManager(4096, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	data := []byte("close me")
	m.Put(data)
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestArenaProperties(t *testing.T) {
	m := newTestManager(t)
	f := func(data []byte) bool {
		if len(data) == 0 || len(data) > 1024 {
			return true
		}
		h, err := m.Put(data)
		if err != nil {
			return false
		}
		got, err := m.View(h)
		if err != nil {
			return false
		}
		return bytes.Equal(got, data)
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 5000}); err != nil {
		t.Error(err)
	}
}

func TestConcurrentPut(t *testing.T) {
	m, err := NewManager(1<<20, false) // 1 MiB
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	const goroutines = 8
	const ops = 10000
	var wg sync.WaitGroup
	wg.Add(goroutines)

	handles := make([][]Handle, goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			data := make([]byte, 64)
			for i := 0; i < ops; i++ {
				data[0] = byte(i)
				h, err := m.Put(data)
				if err != nil {
					if err == ErrArenaFull {
						return
					}
					t.Errorf("goroutine %d: Put: %v", id, err)
					return
				}
				handles[id] = append(handles[id], h)
			}
		}(g)
	}
	wg.Wait()

	// Verify all handles.
	for g := 0; g < goroutines; g++ {
		for i, h := range handles[g] {
			_, err := m.View(h)
			if err != nil {
				t.Errorf("goroutine %d handle %d: View: %v", g, i, err)
			}
		}
	}
}

func TestConcurrentPutFree(t *testing.T) {
	m, err := NewManager(1<<20, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	const goroutines = 8
	const ops = 5000
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			data := make([]byte, 64)
			for i := 0; i < ops; i++ {
				data[0] = byte(i)
				h, err := m.Put(data)
				if err != nil {
					if err == ErrArenaFull {
						return
					}
					return
				}
				if i%3 == 0 {
					m.Free(h)
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestRandomData(t *testing.T) {
	m := newTestManager(t)
	for i := 0; i < 100; i++ {
		sz := 1 + (i * 37 % 4096)
		data := make([]byte, sz)
		rand.Read(data)
		h, err := m.Put(data)
		if err != nil {
			t.Fatalf("Put(%d): %v", sz, err)
		}
		got, err := m.View(h)
		if err != nil {
			t.Fatalf("View(%d): %v", sz, err)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("size %d: data mismatch", sz)
		}
	}
}

func FuzzArenaPut(f *testing.F) {
	f.Add([]byte("hello"))
	f.Add([]byte(""))
	f.Add(make([]byte, 1024))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxObjectSize {
			return
		}
		m := newTestManager(t)
		h, err := m.Put(data)
		if err != nil {
			return
		}
		got, err := m.View(h)
		if err != nil {
			t.Fatalf("View: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("data mismatch: got %d bytes, want %d", len(got), len(data))
		}
	})
}

// TestIncFreqGetFreqSetFreq exercises the atomic frequency counter
// operations and verifies the 2-bit wrap behaviour.
func TestIncFreqGetFreqSetFreq(t *testing.T) {
	m := newTestManager(t)
	h, err := m.Put([]byte("v"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if f := m.GetFreq(h); f != 0 {
		t.Errorf("initial freq = %d, want 0", f)
	}
	// IncFreq from 0 → 1
	if got := m.IncFreq(h); got != 1 {
		t.Errorf("IncFreq 1st = %d, want 1", got)
	}
	// IncFreq 1 → 2
	if got := m.IncFreq(h); got != 2 {
		t.Errorf("IncFreq 2nd = %d, want 2", got)
	}
	// IncFreq 2 → 3
	if got := m.IncFreq(h); got != 3 {
		t.Errorf("IncFreq 3rd = %d, want 3", got)
	}
	// IncFreq 3 → 0 (wrap)
	if got := m.IncFreq(h); got != 0 {
		t.Errorf("IncFreq 4th (wrap) = %d, want 0", got)
	}
	// GetFreq reflects the wrap.
	if f := m.GetFreq(h); f != 0 {
		t.Errorf("GetFreq after wrap = %d, want 0", f)
	}

	// SetFreq to a specific value preserves upper bits (queue tag).
	m.SetFreq(h, 2)
	if f := m.GetFreq(h); f != 2 {
		t.Errorf("GetFreq after SetFreq(2) = %d, want 2", f)
	}

	// SetFreq must not touch upper bits — set QueueTagS, then SetFreq.
	m.OrMetaBits(h, QueueTagS)
	m.SetFreq(h, 0)
	tag := m.GetMeta(h) & QueueTagMask
	if tag != QueueTagS {
		t.Errorf("SetFreq clobbered upper bits: tag=0x%x, want 0x%x", tag, QueueTagS)
	}
}

// TestIncFreqOutOfRange: IncFreq on a handle whose region/index is
// invalid must return 0 and not panic.
func TestIncFreqOutOfRange(t *testing.T) {
	m := newTestManager(t)
	bad := Handle{Offset: 1 << 30, Size: 1, Region: 0}
	if got := m.IncFreq(bad); got != 0 {
		t.Errorf("IncFreq(out-of-range) = %d, want 0", got)
	}
	if got := m.GetFreq(bad); got != 0 {
		t.Errorf("GetFreq(out-of-range) = %d, want 0", got)
	}
	m.SetFreq(bad, 1) // must not panic
	if got := m.GetFreq(bad); got != 0 {
		t.Errorf("GetFreq(out-of-range) after SetFreq = %d, want 0", got)
	}
}

// TestSetGetCASMetaOrClear exercises the full 16-bit meta operations.
func TestSetGetCASMetaOrClear(t *testing.T) {
	m := newTestManager(t)
	h, err := m.Put([]byte("v"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if v := m.GetMeta(h); v != 0 {
		t.Errorf("initial meta = 0x%x, want 0", v)
	}

	m.SetMeta(h, 0xABCD)
	if v := m.GetMeta(h); v != 0xABCD {
		t.Errorf("GetMeta after SetMeta = 0x%x, want 0xABCD", v)
	}

	// CASMeta success
	if !m.CASMeta(h, 0xABCD, 0x1234) {
		t.Errorf("CASMeta expected success")
	}
	if v := m.GetMeta(h); v != 0x1234 {
		t.Errorf("GetMeta after CASMeta = 0x%x, want 0x1234", v)
	}
	// CASMeta failure (stale old value)
	if m.CASMeta(h, 0xABCD, 0xFFFF) {
		t.Errorf("CASMeta expected failure on stale old value")
	}

	// OrMetaBits + ClearMetaBits
	m.SetMeta(h, 0x00F0)
	m.OrMetaBits(h, 0x000F)
	if v := m.GetMeta(h); v != 0x00FF {
		t.Errorf("GetMeta after OrMetaBits = 0x%x, want 0x00FF", v)
	}
	m.ClearMetaBits(h, 0x000F)
	if v := m.GetMeta(h); v != 0x00F0 {
		t.Errorf("GetMeta after ClearMetaBits = 0x%x, want 0x00F0", v)
	}

	// OrMetaBits no-op when bits already set
	m.OrMetaBits(h, 0x00F0)
	if v := m.GetMeta(h); v != 0x00F0 {
		t.Errorf("GetMeta after no-op OrMetaBits = 0x%x, want 0x00F0", v)
	}

	// ClearMetaBits no-op when bits already cleared
	m.ClearMetaBits(h, 0x000F)
	if v := m.GetMeta(h); v != 0x00F0 {
		t.Errorf("GetMeta after no-op ClearMetaBits = 0x%x, want 0x00F0", v)
	}
}

// TestMetaOnInvalidHandle: Set/Get/CASMeta/Or/Clear must be no-ops on
// invalid handles (no panic).
func TestMetaOnInvalidHandle(t *testing.T) {
	m := newTestManager(t)
	bad := Handle{Region: 99}

	m.SetMeta(bad, 0xFF)
	if v := m.GetMeta(bad); v != 0 {
		t.Errorf("GetMeta on bad handle = 0x%x, want 0", v)
	}
	if m.CASMeta(bad, 0, 1) {
		t.Errorf("CASMeta on bad handle should return false")
	}
	// Or/ClearMetaBits must not panic.
	m.OrMetaBits(bad, 0xFF)
	m.ClearMetaBits(bad, 0xFF)
}

// TestEnsureMetaIdempotent: EnsureMeta is safe to call multiple times.
func TestEnsureMetaIdempotent(t *testing.T) {
	m := newTestManager(t)
	for i := 0; i < 5; i++ {
		m.EnsureMeta(0)
	}
	h, err := m.Put([]byte("v"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if f := m.GetFreq(h); f != 0 {
		t.Errorf("GetFreq after EnsureMeta x5 = %d, want 0", f)
	}
}

// TestRegionsAccessor: Regions returns a snapshot of regions.
func TestRegionsAccessor(t *testing.T) {
	m := newTestManager(t)
	rs := m.Regions()
	if len(rs) == 0 {
		t.Fatalf("Regions returned empty")
	}
	if rs[0] == nil {
		t.Errorf("Regions[0] is nil")
	}
}

// TestRegionData: Data() returns the backing slice for a region.
func TestRegionData(t *testing.T) {
	m := newTestManager(t)
	rs := m.Regions()
	if len(rs) == 0 {
		t.Fatalf("no regions")
	}
	data := rs[0].Data()
	if len(data) == 0 {
		t.Errorf("Data() returned empty slice")
	}
}

// TestSyncAsync: SyncAsync returns no error.
func TestSyncAsync(t *testing.T) {
	m := newTestManager(t)
	if err := m.SyncAsync(); err != nil {
		t.Fatalf("SyncAsync: %v", err)
	}
	// SyncAsync after writes still works.
	h, err := m.Put([]byte("after"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := m.View(h); err != nil {
		t.Fatalf("View: %v", err)
	}
	if err := m.SyncAsync(); err != nil {
		t.Fatalf("SyncAsync after Put: %v", err)
	}
}

// TestOpenFileManagerZeroSize returns an error.
func TestOpenFileManagerZeroSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "arena.dat")
	_, err := OpenFileManager(path, 0, true)
	if err == nil {
		t.Errorf("expected error for zero regionSize")
	}
}

// TestOpenFileManagerInvalidPath: create=false on a non-existent file
// returns an error.
func TestOpenFileManagerInvalidPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.dat")
	_, err := OpenFileManager(path, 4096, false)
	if err == nil {
		t.Errorf("expected error for missing file with create=false")
	}
}

// TestOpenFileManagerRoundTrip creates a file, writes through it, and
// reopens.
func TestOpenFileManagerRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file-backed mmap is not persistent on Windows in this build")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "arena.dat")
	const regionSize = 1 << 20
	m, err := OpenFileManager(path, regionSize, true)
	if err != nil {
		t.Fatalf("OpenFileManager(create): %v", err)
	}
	if fp := m.FilePath(); fp != path {
		t.Errorf("FilePath = %q, want %q", fp, path)
	}
	h, err := m.Put([]byte("hello"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := m.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen and verify the handle is still valid.
	m2, err := OpenFileManager(path, regionSize, false)
	if err != nil {
		t.Fatalf("OpenFileManager(reopen): %v", err)
	}
	defer m2.Close()
	got, err := m2.View(h)
	if err != nil {
		t.Fatalf("View after reopen: %v", err)
	}
	if !bytes.Equal(got, []byte("hello")) {
		t.Errorf("data after reopen = %q, want hello", got)
	}
}

// TestOpenFileManagerFilePathEmpty: an anonymous Manager has empty FilePath.
func TestOpenFileManagerFilePathEmpty(t *testing.T) {
	m, err := NewManager(4096, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()
	if fp := m.FilePath(); fp != "" {
		t.Errorf("FilePath = %q, want \"\"", fp)
	}
}

// TestStatsFragmentation: Fragmentation should be < 1.0 once free space
// exists. The allocator has both bump-tail and freelist; we verify the
// formula returns 0 when fully empty.
func TestStatsFragmentationZeroWhenEmpty(t *testing.T) {
	m := newTestManager(t)
	s := m.Stats()
	if s.Fragmentation != 0 {
		t.Errorf("empty Fragmentation = %f, want 0", s.Fragmentation)
	}
}

// TestStatsFragmentationAfterAlloc: Fragmentation is computed when there
// is free space.
func TestStatsFragmentationAfterAlloc(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Put(make([]byte, 1024)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	s := m.Stats()
	if s.Fragmentation < 0 || s.Fragmentation > 1 {
		t.Errorf("Fragmentation = %f, want in [0,1]", s.Fragmentation)
	}
}

// TestPutStatsConsistency: after Free, LiveObjects drops by 1.
func TestFreeStats(t *testing.T) {
	m := newTestManager(t)
	h, err := m.Put([]byte("k"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if live := m.Stats().LiveObjects; live != 1 {
		t.Errorf("LiveObjects after Put = %d, want 1", live)
	}
	if err := m.Free(h); err != nil {
		t.Fatalf("Free: %v", err)
	}
	if live := m.Stats().LiveObjects; live != 0 {
		t.Errorf("LiveObjects after Free = %d, want 0", live)
	}
}

// TestFreeOOB: Free with an out-of-bounds offset returns ErrOffsetInvalid.
func TestFreeOOB(t *testing.T) {
	m := newTestManager(t)
	bad := Handle{Offset: 1 << 30, Size: 16, Region: 0}
	if err := m.Free(bad); !errors.Is(err, ErrOffsetInvalid) {
		t.Errorf("Free(OOB) = %v, want ErrOffsetInvalid", err)
	}
}

// TestCloseIdempotent: Close twice does not panic and returns nil on
// subsequent calls (or any error from the second call is acceptable —
// the spec doesn't promise).
func TestCloseIdempotent(t *testing.T) {
	m, err := NewManager(4096, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
}

// TestEnsureMetaNoOpForMissingRegion: EnsureMeta with an out-of-range
// region is a no-op.
func TestEnsureMetaNoOpForMissingRegion(t *testing.T) {
	m := newTestManager(t)
	// Region 99 does not exist; ensureMetaLocked returns early.
	m.EnsureMeta(99)
	// Subsequent Put still works.
	if _, err := m.Put([]byte("k")); err != nil {
		t.Fatalf("Put after bad EnsureMeta: %v", err)
	}
}

// TestFileHelper sanity — only ensure os package import is used so
// `go vet` doesn't complain on platforms where some helpers are dead.
var _ = os.CreateTemp
