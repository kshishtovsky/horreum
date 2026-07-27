package arena

import (
	"testing"
)

func BenchmarkArenaPut(b *testing.B) {
	sizes := []int{64, 1024, 65536, 1048576}
	for _, sz := range sizes {
		b.Run(formatSize(sz), func(b *testing.B) {
			m, _ := NewManager(1<<30, false) // 1 GiB
			defer m.Close()
			data := make([]byte, sz)
			for i := range data {
				data[i] = byte(i)
			}
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				m.Put(data)
			}
		})
	}
}

func BenchmarkArenaView(b *testing.B) {
	sizes := []int{64, 1024, 65536, 1048576}
	for _, sz := range sizes {
		b.Run(formatSize(sz), func(b *testing.B) {
			m, _ := NewManager(1<<30, false)
			defer m.Close()
			data := make([]byte, sz)
			h, _ := m.Put(data)
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				m.View(h)
			}
		})
	}
}

func BenchmarkArenaPutView(b *testing.B) {
	sizes := []int{64, 1024, 65536, 1048576}
	for _, sz := range sizes {
		b.Run(formatSize(sz), func(b *testing.B) {
			m, _ := NewManager(1<<30, false)
			defer m.Close()
			data := make([]byte, sz)
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				h, _ := m.Put(data)
				m.View(h)
			}
		})
	}
}

func BenchmarkArenaFree(b *testing.B) {
	m, _ := NewManager(1<<30, false)
	defer m.Close()
	data := make([]byte, 64)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h, _ := m.Put(data)
		m.Free(h)
	}
}

func formatSize(n int) string {
	switch {
	case n >= 1<<20:
		return "1M"
	case n >= 1<<16:
		return "64K"
	case n >= 1<<10:
		return "1K"
	default:
		return "64B"
	}
}
