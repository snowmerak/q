package codex

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"

	"github.com/redis/rueidis"
)

const (
	defaultConversationCacheTTL        = 2 * time.Hour
	defaultConversationCacheMaxEntries = 4096
	defaultConversationCacheKeyPrefix  = "llm-provider:codex:conversation:"
)

// ConversationCache stores opaque Codex conversation checkpoints. Provider
// instances close the cache passed through WithConversationCache when they
// close.
type ConversationCache interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, string) error
	Close() error
}

type memoryConversationCacheEntry struct {
	key       string
	value     []byte
	expiresAt time.Time
}

type memoryConversationCache struct {
	mu         sync.Mutex
	maxEntries int
	entries    map[string]*list.Element
	lru        *list.List
	closed     bool
}

// NewMemoryConversationCache creates a bounded in-process LRU cache. A
// non-positive maxEntries value uses the provider default.
func NewMemoryConversationCache(maxEntries int) ConversationCache {
	if maxEntries <= 0 {
		maxEntries = defaultConversationCacheMaxEntries
	}
	return &memoryConversationCache{
		maxEntries: maxEntries,
		entries:    make(map[string]*list.Element, maxEntries),
		lru:        list.New(),
	}
}

func (c *memoryConversationCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, false, errors.New("codex: conversation cache is closed")
	}
	element := c.entries[key]
	if element == nil {
		return nil, false, nil
	}
	entry := element.Value.(*memoryConversationCacheEntry)
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		c.remove(element)
		return nil, false, nil
	}
	c.lru.MoveToFront(element)
	return append([]byte(nil), entry.value...), true, nil
}

func (c *memoryConversationCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("codex: conversation cache is closed")
	}
	expiresAt := time.Time{}
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}
	if element := c.entries[key]; element != nil {
		entry := element.Value.(*memoryConversationCacheEntry)
		entry.value = append(entry.value[:0], value...)
		entry.expiresAt = expiresAt
		c.lru.MoveToFront(element)
		return nil
	}
	entry := &memoryConversationCacheEntry{key: key, value: append([]byte(nil), value...), expiresAt: expiresAt}
	c.entries[key] = c.lru.PushFront(entry)
	for len(c.entries) > c.maxEntries {
		c.remove(c.lru.Back())
	}
	return nil
}

func (c *memoryConversationCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("codex: conversation cache is closed")
	}
	if element := c.entries[key]; element != nil {
		c.remove(element)
	}
	return nil
}

func (c *memoryConversationCache) Close() error {
	c.mu.Lock()
	c.closed = true
	c.entries = nil
	c.lru.Init()
	c.mu.Unlock()
	return nil
}

func (c *memoryConversationCache) remove(element *list.Element) {
	if element == nil {
		return
	}
	entry := element.Value.(*memoryConversationCacheEntry)
	delete(c.entries, entry.key)
	c.lru.Remove(element)
}

// RedisConversationCacheOptions configures a rueidis-backed cache.
type RedisConversationCacheOptions struct {
	Addresses  []string
	Username   string
	Password   string
	Database   int
	ClientName string
	KeyPrefix  string
}

type redisConversationCache struct {
	client rueidis.Client
	prefix string
}

// NewRedisConversationCache creates a shared cache backed by Redis through
// rueidis. At least one Redis address is required.
func NewRedisConversationCache(options RedisConversationCacheOptions) (ConversationCache, error) {
	if len(options.Addresses) == 0 {
		return nil, errors.New("codex: redis conversation cache requires at least one address")
	}
	client, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress: append([]string(nil), options.Addresses...),
		Username:    options.Username,
		Password:    options.Password,
		SelectDB:    options.Database,
		ClientName:  options.ClientName,
	})
	if err != nil {
		return nil, err
	}
	prefix := options.KeyPrefix
	if prefix == "" {
		prefix = defaultConversationCacheKeyPrefix
	}
	return &redisConversationCache{client: client, prefix: prefix}, nil
}

func (c *redisConversationCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	value, err := c.client.Do(ctx, c.client.B().Get().Key(c.prefix+key).Build()).ToString()
	if rueidis.IsRedisNil(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []byte(value), true, nil
}

func (c *redisConversationCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	builder := c.client.B().Set().Key(c.prefix + key).Value(string(value))
	if ttl > 0 {
		return c.client.Do(ctx, builder.Px(ttl).Build()).Error()
	}
	return c.client.Do(ctx, builder.Build()).Error()
}

func (c *redisConversationCache) Delete(ctx context.Context, key string) error {
	return c.client.Do(ctx, c.client.B().Del().Key(c.prefix+key).Build()).Error()
}

func (c *redisConversationCache) Close() error {
	c.client.Close()
	return nil
}

var _ ConversationCache = (*memoryConversationCache)(nil)
var _ ConversationCache = (*redisConversationCache)(nil)
