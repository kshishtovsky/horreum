// Package transport owns the shared-nothing shard manager and the
// in-process CacheService implementation.  It depends on
// internal/transport/api for the public Transport interface; this
// keeps the import graph acyclic when tcp and quic sub-packages both
// depend on api.
package transport

import (
	"errors"
	"hash/fnv"
	"net"
	"runtime"
	"sync/atomic"

	"github.com/horreum/horreum/internal/arena"
	"github.com/horreum/horreum/internal/eviction"
	"github.com/horreum/horreum/internal/transport/api"
)

// Public type aliases so callers can use transport.CacheService
// without importing api directly.
type (
	CacheService = api.CacheService
	ShardRouter  = api.ShardRouter
)

// Sentinel errors.
var (
	ErrNotFound = api.ErrNotFound
	ErrTooLarge = api.ErrTooLarge
)

// errBackendNil is internal.
var errBackendNil = errors.New("transport: shardCache is nil")

// shardCache is one shared-nothing cache instance.
type shardCache struct {
	mgr *arena.Manager
	ev  *eviction.Eviction
	idx *shardIndex
}

// Compile-time check.
var _ CacheService = (*shardCache)(nil)

// NewShardCache creates a cache shard with the given arena region size
// and eviction capacity.
func NewShardCache(regionSize, evictCapacity uint64) (*shardCache, error) {
	if regionSize == 0 {
		regionSize = 64 << 20
	}
	if evictCapacity == 0 {
		evictCapacity = 1024
	}
	mgr, err := arena.NewManager(regionSize, false)
	if err != nil {
		return nil, err
	}
	return &shardCache{
		mgr: mgr,
		ev:  eviction.New(mgr, evictCapacity),
		idx: newShardIndex(),
	}, nil
}

// Set implements CacheService.
func (s *shardCache) Set(key, value []byte) ([]byte, error) {
	if s == nil {
		return nil, errBackendNil
	}
	if uint64(len(value)) > arena.MaxObjectSize {
		return nil, ErrTooLarge
	}
	h, err := s.mgr.Put(value)
	if err != nil {
		return nil, err
	}
	for _, e := range s.ev.Add(key, h) {
		_ = s.mgr.Free(e)
	}
	old, replaced := s.idx.put(key, h)
	if replaced {
		_ = s.mgr.Free(old)
	}
	return s.mgr.View(h)
}

// Get implements CacheService.  Touch on hit updates the S3-FIFO freq.
func (s *shardCache) Get(key []byte) ([]byte, error) {
	if s == nil {
		return nil, errBackendNil
	}
	h, ok := s.idx.get(key)
	if !ok {
		return nil, ErrNotFound
	}
	s.ev.Touch(h)
	return s.mgr.View(h)
}

// Delete implements CacheService.
func (s *shardCache) Delete(key []byte) error {
	if s == nil {
		return errBackendNil
	}
	h, ok := s.idx.delete(key)
	if !ok {
		return nil
	}
	s.ev.Delete(key, h)
	return s.mgr.Free(h)
}

// Close releases the arena region.
func (s *shardCache) Close() error {
	if s == nil {
		return nil
	}
	return s.mgr.Close()
}

// shardIndex is a minimal key→handle hash index.  Not concurrent-safe.
type shardIndex struct {
	keys    [][]byte
	handles []arena.Handle
	count   int
	mask    uint64
}

func newShardIndex() *shardIndex {
	const initial = 64
	return &shardIndex{
		keys:    make([][]byte, initial),
		handles: make([]arena.Handle, initial),
		mask:    initial - 1,
	}
}

func (s *shardIndex) put(key []byte, h arena.Handle) (arena.Handle, bool) {
	if s.count*4 >= len(s.keys)*3 {
		s.grow()
	}
	i := hashKey(key) & s.mask
	for {
		if s.keys[i] == nil {
			s.keys[i] = key
			s.handles[i] = h
			s.count++
			return arena.Handle{}, false
		}
		if bytesEq(s.keys[i], key) {
			old := s.handles[i]
			s.handles[i] = h
			return old, true
		}
		i = (i + 1) & s.mask
	}
}

func (s *shardIndex) get(key []byte) (arena.Handle, bool) {
	if s.count == 0 {
		return arena.Handle{}, false
	}
	i := hashKey(key) & s.mask
	for {
		if s.keys[i] == nil {
			return arena.Handle{}, false
		}
		if bytesEq(s.keys[i], key) {
			return s.handles[i], true
		}
		i = (i + 1) & s.mask
	}
}

func (s *shardIndex) delete(key []byte) (arena.Handle, bool) {
	if s.count == 0 {
		return arena.Handle{}, false
	}
	i := hashKey(key) & s.mask
	for {
		if s.keys[i] == nil {
			return arena.Handle{}, false
		}
		if bytesEq(s.keys[i], key) {
			old := s.handles[i]
			s.keys[i] = nil
			s.handles[i] = arena.Handle{}
			s.count--
			s.rehash((i + 1) & s.mask)
			return old, true
		}
		i = (i + 1) & s.mask
	}
}

func (s *shardIndex) rehash(start uint64) {
	i := start
	for {
		if s.keys[i] == nil {
			return
		}
		k := s.keys[i]
		h := s.handles[i]
		desired := hashKey(k) & s.mask
		if s.canMove(desired, i, start) {
			gap := s.findGap(desired, i)
			if gap != i {
				s.keys[gap] = k
				s.handles[gap] = h
				s.keys[i] = nil
				s.handles[i] = arena.Handle{}
				continue
			}
		}
		i = (i + 1) & s.mask
	}
}

func (s *shardIndex) canMove(desired, current, gap uint64) bool {
	if desired <= gap {
		return current >= gap || current < desired
	}
	return current >= gap && current < desired
}

func (s *shardIndex) findGap(desired, current uint64) uint64 {
	gap := desired
	for gap != current {
		if s.keys[gap] == nil {
			return gap
		}
		gap = (gap + 1) & s.mask
	}
	return current
}

func (s *shardIndex) grow() {
	oldKeys := s.keys
	oldHandles := s.handles
	nc := uint64(len(oldKeys)) * 2
	s.keys = make([][]byte, nc)
	s.handles = make([]arena.Handle, nc)
	s.mask = nc - 1
	s.count = 0
	for i, k := range oldKeys {
		if k != nil {
			s.put(k, oldHandles[i])
		}
	}
}

func bytesEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// hashKey is FNV-1a 64.
func hashKey(key []byte) uint64 {
	const (
		offset uint64 = 14695981039346656037
		prime  uint64 = 1099511628211
	)
	h := offset
	for _, b := range key {
		h ^= uint64(b)
		h *= prime
	}
	return h
}

// ──────────────────────────────────────────────────────────────────────
// ShardSet — routes keys to shards and exposes CacheFor.
// ──────────────────────────────────────────────────────────────────────

// ShardConfig configures a ShardSet.
type ShardConfig struct {
	// NumShards is the number of shards.  Zero means
	// DefaultShardCount (4 × NumCPU, bounded 4..64).
	NumShards int
	// RegionSize is the arena region size per shard in bytes.
	// Zero means 64 MiB.
	RegionSize uint64
	// EvictCapacity is the S3-FIFO eviction capacity per shard
	// (number of objects).
	EvictCapacity uint64
}

// DefaultShardCount returns 4 × NumCPU, clamped to [4, 64].
func DefaultShardCount() int {
	n := runtime.NumCPU() * 4
	if n < 4 {
		n = 4
	}
	if n > 64 {
		n = 64
	}
	return n
}

// ShardSet is a shared-nothing collection of cache shards.  CacheFor
// routes a key to a shard via FNV-1a(key) modulo NumShards.
type ShardSet struct {
	shards []*shardCache
	closed atomic.Bool
}

// Compile-time check.
var _ ShardRouter = (*ShardSet)(nil)

// NewShardSet creates N independent shards.
func NewShardSet(cfg ShardConfig) (*ShardSet, error) {
	if cfg.NumShards == 0 {
		cfg.NumShards = DefaultShardCount()
	}
	ss := &ShardSet{shards: make([]*shardCache, cfg.NumShards)}
	for i := 0; i < cfg.NumShards; i++ {
		c, err := NewShardCache(cfg.RegionSize, cfg.EvictCapacity)
		if err != nil {
			_ = ss.Close()
			return nil, err
		}
		ss.shards[i] = c
	}
	return ss, nil
}

// CacheFor returns the cache whose shard owns key.
func (ss *ShardSet) CacheFor(key []byte) CacheService {
	if ss == nil || len(ss.shards) == 0 {
		return nil
	}
	return ss.shards[hashKey(key)%uint64(len(ss.shards))]
}

// ShardCount returns the number of shards.
func (ss *ShardSet) ShardCount() int {
	if ss == nil {
		return 0
	}
	return len(ss.shards)
}

// PickByAddr returns the shard index for a remote address.  Used by
// transports that pin connections by source IP.
func (ss *ShardSet) PickByAddr(addr net.Addr) int {
	if ss == nil || len(ss.shards) == 0 {
		return 0
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(host))
	return int(h.Sum64() % uint64(len(ss.shards)))
}

// Close releases every shard's arena region.
func (ss *ShardSet) Close() error {
	if ss == nil {
		return nil
	}
	if !ss.closed.CompareAndSwap(false, true) {
		return nil
	}
	for _, s := range ss.shards {
		_ = s.Close()
	}
	return nil
}
