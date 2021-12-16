package bigcache

import "time"

// Config for BigCache
type Config struct {
	// Number of cache shards, value must be a power of two
	////缓存碎片数，值必须是2的幂
	Shards int
	// Time after which entry can be evicted
	LifeWindow time.Duration
	// Interval between removing expired entries (clean up).
	// If set to <= 0 then no action is performed. Setting to < 1 second is counterproductive — bigcache has a one second resolution.
	////如果设置为<=0，则不执行任何操作。设置为<1秒会适得其反-bigcache的分辨率为1秒。
	CleanWindow time.Duration
	// Max number of entries in life window. Used only to calculate initial size for cache shards.
	// When proper value is set then additional memory allocation does not occur.
	//生命窗口中的最大条目数。仅用于计算缓存碎片的初始大小。
	//如果设置了正确的值，则不会发生额外的内存分配。
	MaxEntriesInWindow int
	// Max size of entry in bytes. Used only to calculate initial size for cache shards.
	////条目的最大大小（字节）。仅用于计算缓存碎片的初始大小。
	MaxEntrySize int
	// StatsEnabled if true calculate the number of times a cached resource was requested.
	StatsEnabled bool
	// Verbose mode prints information about new memory allocation
	//详细模式打印有关新内存分配的信息
	Verbose bool
	// Hasher used to map between string keys and unsigned 64bit integers, by default fnv64 hashing is used.
	Hasher Hasher
	// HardMaxCacheSize is a limit for BytesQueue size in MB.
	// It can protect application from consuming all available memory on machine, therefore from running OOM Killer.
	// Default value is 0 which means unlimited size. When the limit is higher than 0 and reached then
	// the oldest entries are overridden for the new ones. The max memory consumption will be bigger than
	// HardMaxCacheSize due to Shards' s additional memory. Every Shard consumes additional memory for map of keys
	// and statistics (map[uint64]uint32) the size of this map is equal to number of entries in
	// cache ~ 2×(64+32)×n bits + overhead or map itself.
	//HardMaxCacheSize是字节队列大小（MB）的限制。
	//它可以保护应用程序不消耗机器上的所有可用内存，从而不运行OOM Killer。
	//默认值为0，表示大小不受限制。当限值高于0并达到时
	//最旧的条目将被新条目覆盖。最大内存消耗将大于
	//HardMaxCacheSize由于碎片的额外内存。每个碎片都会为密钥映射消耗额外的内存
	//和统计（map[uint64]uint32）此映射的大小等于
	//缓存~2×（64+32）×n位+开销或映射本身。
	HardMaxCacheSize int
	// OnRemove is a callback fired when the oldest entry is removed because of its expiration time or no space left
	// for the new entry, or because delete was called.
	// Default value is nil which means no callback and it prevents from unwrapping the oldest entry.
	// ignored if OnRemoveWithMetadata is specified.
	OnRemove func(key string, entry []byte)
	// OnRemoveWithMetadata is a callback fired when the oldest entry is removed because of its expiration time or no space left
	// for the new entry, or because delete was called. A structure representing details about that specific entry.
	// Default value is nil which means no callback and it prevents from unwrapping the oldest entry.
	OnRemoveWithMetadata func(key string, entry []byte, keyMetadata Metadata)
	// OnRemoveWithReason is a callback fired when the oldest entry is removed because of its expiration time or no space left
	// for the new entry, or because delete was called. A constant representing the reason will be passed through.
	// Default value is nil which means no callback and it prevents from unwrapping the oldest entry.
	// Ignored if OnRemove is specified.
	OnRemoveWithReason func(key string, entry []byte, reason RemoveReason)

	onRemoveFilter int

	// Logger is a logging interface and used in combination with `Verbose`
	// Defaults to `DefaultLogger()`
	Logger Logger
}

// DefaultConfig initializes config with default values.
// When load for BigCache can be predicted in advance then it is better to use custom config.
func DefaultConfig(eviction time.Duration) Config {
	return Config{
		Shards:             1024,
		LifeWindow:         eviction,
		CleanWindow:        1 * time.Second,
		MaxEntriesInWindow: 1000 * 10 * 60,
		MaxEntrySize:       500,
		StatsEnabled:       false,
		Verbose:            true,
		Hasher:             newDefaultHasher(),
		HardMaxCacheSize:   0,
		Logger:             DefaultLogger(),
	}
}

// initialShardSize computes initial shard size
func (c Config) initialShardSize() int {
	return max(c.MaxEntriesInWindow/c.Shards, minimumEntriesInShard)
}

// maximumShardSizeInBytes computes maximum shard size in bytes
func (c Config) maximumShardSizeInBytes() int {
	maxShardSize := 0

	if c.HardMaxCacheSize > 0 {
		maxShardSize = convertMBToBytes(c.HardMaxCacheSize) / c.Shards
	}

	return maxShardSize
}

// OnRemoveFilterSet sets which remove reasons will trigger a call to OnRemoveWithReason.
// Filtering out reasons prevents bigcache from unwrapping them, which saves cpu.
func (c Config) OnRemoveFilterSet(reasons ...RemoveReason) Config {
	c.onRemoveFilter = 0
	for i := range reasons {
		c.onRemoveFilter |= 1 << uint(reasons[i])
	}

	return c
}
