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
	"github.com/horreum/horreum/internal/compress"
	"github.com/horreum/horreum/internal/encrypt"
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
	mgr    *arena.Manager
	ev     *eviction.Eviction
	idx    *shardIndex
	comp   compress.Compressor // nil treated as Noop
	cipher encrypt.Cipher      // nil treated as Noop
}

// Compile-time check.
var _ CacheService = (*shardCache)(nil)

// NewShardCache creates a cache shard with the given arena region size
// and eviction capacity.  comp and cipher may be nil (no compression/encryption).
func NewShardCache(regionSize, evictCapacity uint64, comp compress.Compressor, cipher encrypt.Cipher) (*shardCache, error) {
	if regionSize == 0 {
		regionSize = 64 << 20
	}
	if evictCapacity == 0 {
		evictCapacity = 1024
	}
	if comp == nil {
		comp = compress.Noop{}
	}
	if cipher == nil {
		cipher = encrypt.Noop{}
	}
	mgr, err := arena.NewManager(regionSize, false)
	if err != nil {
		return nil, err
	}
	return &shardCache{
		mgr:    mgr,
		ev:     eviction.New(mgr, evictCapacity),
		idx:    newShardIndex(),
		comp:   comp,
		cipher: cipher,
	}, nil
}

// newShardCacheFromArena wraps an existing arena.Manager instead of
// allocating a new region.  Used by the persistent-mode ShardSet.
func newShardCacheFromArena(mgr *arena.Manager, evictCapacity uint64, comp compress.Compressor, cipher encrypt.Cipher) *shardCache {
	if evictCapacity == 0 {
		evictCapacity = 1024
	}
	if comp == nil {
		comp = compress.Noop{}
	}
	if cipher == nil {
		cipher = encrypt.Noop{}
	}
	return &shardCache{
		mgr:    mgr,
		ev:     eviction.New(mgr, evictCapacity),
		idx:    newShardIndex(),
		comp:   comp,
		cipher: cipher,
	}
}

// Set implements CacheService.
// If compression is enabled and the value exceeds the minimum
// threshold, the value is compressed before storing in the arena.
// The CompressedFlag meta bit is set so Get knows to decompress.
func (s *shardCache) Set(key, value []byte) ([]byte, error) {
	if s == nil {
		return nil, errBackendNil
	}
	if uint64(len(value)) > arena.MaxObjectSize {
		return nil, ErrTooLarge
	}

	// 1. Try compression.
	storeVal := value
	compressed := false
	if cBuf, ok := s.comp.Compress(value); ok {
		storeVal = cBuf
		compressed = true
		// cBuf is a pool buffer; we copy into the arena below,
		// then return it.
		defer s.comp.PutBuf(cBuf)
	}

	// 2. Try encryption.
	encrypted := false
	if s.cipher != nil && s.cipher.Name() != "none" {
		encBuf, err := s.cipher.Encrypt(nil, storeVal)
		if err != nil {
			return nil, err
		}
		storeVal = encBuf
		encrypted = true
	}

	h, err := s.mgr.Put(storeVal)
	if err != nil {
		return nil, err
	}
	if compressed {
		s.mgr.OrMetaBits(h, arena.CompressedFlag)
	}
	if encrypted {
		s.mgr.OrMetaBits(h, arena.EncryptedFlag)
	}
	for _, e := range s.ev.Add(key, h) {
		_ = s.mgr.Free(e)
	}
	old, replaced := s.idx.put(key, h)
	if replaced {
		_ = s.mgr.Free(old)
	}
	// Return the original (uncompressed, unencrypted) value to the caller.
	if compressed || encrypted {
		// Make a copy of the original value — the caller expects
		// to read the value it just set.
		out := make([]byte, len(value))
		copy(out, value)
		return out, nil
	}
	return s.mgr.View(h)
}

// Get implements CacheService.  Touch on hit updates the S3-FIFO freq.
//
// If the stored value has the EncryptedFlag set, it is decrypted.
// If the stored value has the CompressedFlag set, it is decompressed.
func (s *shardCache) Get(key []byte) ([]byte, error) {
	if s == nil {
		return nil, errBackendNil
	}
	h, ok := s.idx.get(key)
	if !ok {
		return nil, ErrNotFound
	}
	s.ev.Touch(h)
	raw, err := s.mgr.View(h)
	if err != nil {
		return nil, err
	}

	// 1. Decrypt if the encrypted flag is set.
	if s.mgr.GetMeta(h)&arena.EncryptedFlag != 0 {
		if s.cipher == nil || s.cipher.Name() == "none" {
			return nil, errors.New("transport: data is encrypted but no cipher key is configured")
		}
		dec, err := s.cipher.Decrypt(nil, raw)
		if err != nil {
			return nil, err
		}
		raw = dec
	}

	// 2. Decompress if the compressed flag is set.
	if s.mgr.GetMeta(h)&arena.CompressedFlag != 0 {
		dec, err := s.comp.Decompress(raw)
		if err != nil {
			return nil, err
		}
		return dec, nil
	}
	return raw, nil
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
	// Compressor is the optional value compressor.  nil means no
	// compression (Noop).  Shared across all shards.
	Compressor compress.Compressor
	// Cipher is the optional value cipher.  nil means no
	// encryption (Noop).  Shared across all shards.
	Cipher encrypt.Cipher
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
		c, err := NewShardCache(cfg.RegionSize, cfg.EvictCapacity, cfg.Compressor, cfg.Cipher)
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

// CacheIndexFor returns the shard index that owns key.
func (ss *ShardSet) CacheIndexFor(key []byte) int {
	if ss == nil || len(ss.shards) == 0 {
		return 0
	}
	return int(hashKey(key) % uint64(len(ss.shards)))
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

// Stats returns aggregated arena stats across all shards.
func (ss *ShardSet) Stats() arena.Stats {
	if ss == nil {
		return arena.Stats{}
	}
	var out arena.Stats
	for _, s := range ss.shards {
		if s == nil {
			continue
		}
		// Each shardCache wraps an arena.Manager; we read its
		// Stats() directly.
		sss := s.mgr.Stats()
		out.UsedBytes += sss.UsedBytes
		out.FreeBytes += sss.FreeBytes
		out.LiveObjects += sss.LiveObjects
		out.FreelistLen += sss.FreelistLen
	}
	return out
}

// NewShardSetOrPanic is the same as NewShardSet but panics on
// failure.  Used by the CLI which has no recovery path.
func NewShardSetOrPanic(numShards int, regionSize, evictCap uint64, comp compress.Compressor, cipher encrypt.Cipher) *ShardSet {
	ss, err := NewShardSet(ShardConfig{
		NumShards:     numShards,
		RegionSize:    regionSize,
		EvictCapacity: evictCap,
		Compressor:    comp,
		Cipher:        cipher,
	})
	if err != nil {
		panic(err)
	}
	return ss
}

// NewShardSetFromArena wraps an existing arena.Manager in a ShardSet
// with a single shard.  The shard reuses the supplied arena instead
// of allocating its own region.
//
// Used by the persistent-mode path in cmd/horreum, which owns the
// arena Manager via PersistentManager and wants to feed it directly
// into the transport layer.
func NewShardSetFromArena(mgr *arena.Manager, numShards int, evictCap uint64, comp compress.Compressor, cipher encrypt.Cipher) *ShardSet {
	if numShards <= 0 {
		numShards = 1
	}
	if evictCap == 0 {
		evictCap = 1024
	}
	ss := &ShardSet{shards: make([]*shardCache, numShards)}
	for i := 0; i < numShards; i++ {
		// Wrap the shared arena Manager.  Each shard gets its own
		// shardIndex; the arena is shared.
		ss.shards[i] = newShardCacheFromArena(mgr, evictCap, comp, cipher)
	}
	return ss
}
