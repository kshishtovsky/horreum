package arena

import (
	"bytes"
	"crypto/rand"
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
