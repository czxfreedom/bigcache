package bigcache

import (
	"fmt"
	"time"
)

const (
	minimumEntriesInShard = 10 // Minimum number of entries in single shard
)

// BigCache is fast, concurrent, evicting cache created to keep big number of entries without impact on performance.
// It keeps entries on heap but omits GC for them. To achieve that, operations take place on byte arrays,
// therefore entries (de)serialization in front of the cache will be needed in most use cases.
type BigCache struct {
	shards     []*cacheShard //分片
	lifeWindow uint64        //过期时间
	clock      clock         //时间获取的方法
	hash       Hasher        //hash算法
	config     Config        //配置文件
	shardMask  uint64        //常量：分片数-1，用于&运算替换取模运算
	close      chan struct{} //控制关闭
}

// Response will contain metadata about the entry for which GetWithInfo(key) was called
type Response struct {
	EntryStatus RemoveReason
}

// RemoveReason is a value used to signal to the user why a particular key was removed in the OnRemove callback.
type RemoveReason uint32

const (
	// Expired means the key is past its LifeWindow.
	Expired = RemoveReason(1)
	// NoSpace means the key is the oldest and the cache size was at its maximum when Set was called, or the
	// entry exceeded the maximum shard size.
	NoSpace = RemoveReason(2)
	// Deleted means Delete was called and this key was removed as a result.
	Deleted = RemoveReason(3)
)

// NewBigCache initialize new instance of BigCache
func NewBigCache(config Config) (*BigCache, error) {
	return newBigCache(config, &systemClock{})
}

func newBigCache(config Config, clock clock) (*BigCache, error) {
	//碎片数必须是2的幂次方
	if !isPowerOfTwo(config.Shards) {
		return nil, fmt.Errorf("Shards number must be power of two")
	}
	if config.MaxEntrySize < 0 {
		return nil, fmt.Errorf("MaxEntrySize must be >= 0")
	}
	if config.MaxEntriesInWindow < 0 {
		return nil, fmt.Errorf("MaxEntriesInWindow must be >= 0")
	}
	if config.HardMaxCacheSize < 0 {
		return nil, fmt.Errorf("HardMaxCacheSize must be >= 0")
	}

	if config.Hasher == nil {
		config.Hasher = newDefaultHasher()
	}

	cache := &BigCache{
		shards:     make([]*cacheShard, config.Shards),
		lifeWindow: uint64(config.LifeWindow.Seconds()),
		clock:      clock,
		hash:       config.Hasher,
		config:     config,
		shardMask:  uint64(config.Shards - 1),
		close:      make(chan struct{}),
	}
	//设置回调函数
	var onRemove func(wrappedEntry []byte, reason RemoveReason)
	if config.OnRemoveWithMetadata != nil {
		onRemove = cache.providedOnRemoveWithMetadata
	} else if config.OnRemove != nil {
		onRemove = cache.providedOnRemove
	} else if config.OnRemoveWithReason != nil {
		onRemove = cache.providedOnRemoveWithReason
	} else {
		onRemove = cache.notProvidedOnRemove
	}

	for i := 0; i < config.Shards; i++ {
		cache.shards[i] = initNewShard(config, onRemove, clock)
	}

	//定时删除过期缓存协程
	if config.CleanWindow > 0 {
		go func() {
			ticker := time.NewTicker(config.CleanWindow)
			defer ticker.Stop()
			for {
				select {
				case t := <-ticker.C:
					cache.cleanUp(uint64(t.Unix()))
				case <-cache.close:
					return
				}
			}
		}()
	}

	return cache, nil
}

// Close is used to signal a shutdown of the cache when you are done with it.
// This allows the cleaning goroutines to exit and ensures references are not
// kept to the cache preventing GC of the entire cache.
func (c *BigCache) Close() error {
	close(c.close)
	return nil
}

// Get reads entry for the key.
// It returns an ErrEntryNotFound when
// no entry exists for the given key.
func (c *BigCache) Get(key string) ([]byte, error) {
	hashedKey := c.hash.Sum64(key)   //计算key的hash值
	shard := c.getShard(hashedKey)   //确定分片
	return shard.get(key, hashedKey) //执行分片的get方法
}

// GetWithInfo reads entry for the key with Response info.
// It returns an ErrEntryNotFound when
// no entry exists for the given key.
func (c *BigCache) GetWithInfo(key string) ([]byte, Response, error) {
	hashedKey := c.hash.Sum64(key)
	shard := c.getShard(hashedKey)
	return shard.getWithInfo(key, hashedKey)
}

// Set saves entry under the key
func (c *BigCache) Set(key string, entry []byte) error {
	hashedKey := c.hash.Sum64(key)          ////计算key的hash值
	shard := c.getShard(hashedKey)          //确定分片
	return shard.set(key, hashedKey, entry) //执行分片的set方法
}

// Append appends entry under the key if key exists, otherwise
// it will set the key (same behaviour as Set()). With Append() you can
// concatenate multiple entries under the same key in an lock optimized way.
func (c *BigCache) Append(key string, entry []byte) error {
	hashedKey := c.hash.Sum64(key)
	shard := c.getShard(hashedKey)
	return shard.append(key, hashedKey, entry)
}

// Delete removes the key
func (c *BigCache) Delete(key string) error {
	hashedKey := c.hash.Sum64(key)
	shard := c.getShard(hashedKey)
	return shard.del(hashedKey)
}

// Reset empties all cache shards
func (c *BigCache) Reset() error {
	for _, shard := range c.shards {
		shard.reset(c.config)
	}
	return nil
}

// Len computes number of entries in cache
func (c *BigCache) Len() int {
	var len int
	for _, shard := range c.shards {
		len += shard.len()
	}
	return len
}

// Capacity returns amount of bytes store in the cache.
func (c *BigCache) Capacity() int {
	var len int
	for _, shard := range c.shards {
		len += shard.capacity()
	}
	return len
}

// Stats returns cache's statistics
func (c *BigCache) Stats() Stats {
	var s Stats
	for _, shard := range c.shards {
		tmp := shard.getStats()
		s.Hits += tmp.Hits
		s.Misses += tmp.Misses
		s.DelHits += tmp.DelHits
		s.DelMisses += tmp.DelMisses
		s.Collisions += tmp.Collisions
	}
	return s
}

// KeyMetadata returns number of times a cached resource was requested.
func (c *BigCache) KeyMetadata(key string) Metadata {
	hashedKey := c.hash.Sum64(key)
	shard := c.getShard(hashedKey)
	return shard.getKeyMetadataWithLock(hashedKey)
}

// Iterator returns iterator function to iterate over EntryInfo's from whole cache.
func (c *BigCache) Iterator() *EntryInfoIterator {
	return newIterator(c)
}

func (c *BigCache) onEvict(oldestEntry []byte, currentTimestamp uint64, evict func(reason RemoveReason) error) bool {
	oldestTimestamp := readTimestampFromEntry(oldestEntry)
	if currentTimestamp-oldestTimestamp > c.lifeWindow {
		evict(Expired)
		return true
	}
	return false
}

func (c *BigCache) cleanUp(currentTimestamp uint64) {
	for _, shard := range c.shards {
		shard.cleanUp(currentTimestamp)
	}
}

//这样设计的好处就是计算余数可以使用位运算快速计算
//根据key,获取hash在哪块分片上
func (c *BigCache) getShard(hashedKey uint64) (shard *cacheShard) {
	//hashedKey&c.shardMask == hashedkey%c.shards
	return c.shards[hashedKey&c.shardMask]
}

func (c *BigCache) providedOnRemove(wrappedEntry []byte, reason RemoveReason) {
	c.config.OnRemove(readKeyFromEntry(wrappedEntry), readEntry(wrappedEntry))
}

func (c *BigCache) providedOnRemoveWithReason(wrappedEntry []byte, reason RemoveReason) {
	if c.config.onRemoveFilter == 0 || (1<<uint(reason))&c.config.onRemoveFilter > 0 {
		c.config.OnRemoveWithReason(readKeyFromEntry(wrappedEntry), readEntry(wrappedEntry), reason)
	}
}

func (c *BigCache) notProvidedOnRemove(wrappedEntry []byte, reason RemoveReason) {
}

func (c *BigCache) providedOnRemoveWithMetadata(wrappedEntry []byte, reason RemoveReason) {
	hashedKey := c.hash.Sum64(readKeyFromEntry(wrappedEntry))
	shard := c.getShard(hashedKey)
	c.config.OnRemoveWithMetadata(readKeyFromEntry(wrappedEntry), readEntry(wrappedEntry), shard.getKeyMetadata(hashedKey))
}
