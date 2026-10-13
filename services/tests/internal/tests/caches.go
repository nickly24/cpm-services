package tests

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	cacheMaxEntries = 1024
	cacheMaxBytes   = 64 << 20
	cacheEntryLimit = 2 << 20
	documentTTL     = 20 * time.Second
	catalogTTL      = 20 * time.Second
	contentTTL      = time.Hour
)

// This cache holds data payloads, never actors, permissions, attempt state or
// computed rating snapshots. Mutable payload hits require a live Mongo revision
// check. Serialized values make every returned object independent of its cache.
type dataCache struct {
	mu         sync.Mutex
	entries    map[string]*list.Element
	lru        *list.List
	bytes      int
	maxEntries int
	maxBytes   int
	generation uint64
	now        func() time.Time
}
type cachedPayload struct {
	key     string
	value   []byte
	expires time.Time
}

func newDataCache() *dataCache {
	return &dataCache{entries: map[string]*list.Element{}, lru: list.New(), maxEntries: cacheMaxEntries, maxBytes: cacheMaxBytes, now: time.Now}
}
func (c *dataCache) epoch() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}
func (c *dataCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	if e == nil {
		return nil, false
	}
	item := e.Value.(*cachedPayload)
	if !c.now().Before(item.expires) {
		c.remove(e)
		return nil, false
	}
	c.lru.MoveToFront(e)
	return append([]byte(nil), item.value...), true
}
func (c *dataCache) put(key string, value []byte, ttl time.Duration, generation uint64) bool {
	if len(value) == 0 || len(value) > cacheEntryLimit || ttl <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// A Mongo read that started before a successful CAS cannot repopulate a
	// previously invalidated document/catalog after that mutation completes.
	if c.generation != generation || len(value) > c.maxBytes || c.maxEntries < 1 {
		return false
	}
	if e := c.entries[key]; e != nil {
		c.remove(e)
	}
	for c.lru.Len() >= c.maxEntries || c.bytes+len(value) > c.maxBytes {
		c.remove(c.lru.Back())
	}
	item := &cachedPayload{key: key, value: append([]byte(nil), value...), expires: c.now().Add(ttl)}
	c.entries[key] = c.lru.PushFront(item)
	c.bytes += len(value)
	return true
}
func (c *dataCache) remove(e *list.Element) {
	if e == nil {
		return
	}
	item := e.Value.(*cachedPayload)
	delete(c.entries, item.key)
	c.bytes -= len(item.value)
	c.lru.Remove(e)
}
func (c *dataCache) invalidate(org int64, id bson.ObjectID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	documentPrefix := fmt.Sprintf("document:%d:%s:", org, id.Hex())
	catalogPrefix := fmt.Sprintf("catalog:%d:", org)
	for key, e := range c.entries {
		if strings.HasPrefix(key, documentPrefix) || strings.HasPrefix(key, catalogPrefix) {
			c.remove(e)
		}
	}
}
func (s *Store) cache() *dataCache {
	s.cacheOnce.Do(func() { s.payloadCache = newDataCache() })
	return s.payloadCache
}
func documentCacheKey(org int64, id bson.ObjectID, revision int64) string {
	return fmt.Sprintf("document:%d:%s:%d", org, id.Hex(), revision)
}
func contentCacheKey(org int64, id bson.ObjectID) string {
	return fmt.Sprintf("content:%d:%s", org, id.Hex())
}
func safeContentCacheKey(org int64, id bson.ObjectID) string {
	return fmt.Sprintf("safe-content:%d:%s", org, id.Hex())
}

type documentStamp struct {
	ID             bson.ObjectID `bson:"_id"`
	OrganizationID int64         `bson:"organization_id"`
	Revision       int64         `bson:"revision"`
}
type catalogPayload struct {
	Stamps []documentStamp `bson:"stamps"`
	Items  []Document      `bson:"items"`
}

func catalogCacheKey(org int64, filter bson.M, skip, limit int64) string {
	return fmt.Sprintf("catalog:%d:%s", org, hashJSON(struct {
		Filter bson.M `json:"filter"`
		Skip   int64  `json:"skip"`
		Limit  int64  `json:"limit"`
	}{filter, skip, limit}))
}
func stampsForDocuments(items []Document) []documentStamp {
	stamps := make([]documentStamp, 0, len(items))
	for _, d := range items {
		stamps = append(stamps, documentStamp{d.ID, d.OrganizationID, d.Revision})
	}
	return stamps
}

func cacheableStamps(org int64, stamps []documentStamp) bool {
	for _, stamp := range stamps {
		if stamp.ID.IsZero() || stamp.OrganizationID != org || stamp.Revision < 1 {
			return false
		}
	}
	return true
}

// LoadSafeQuestions caches only the allowlisted student DTO, independently of
// the full immutable version. Callers must still authorize the live attempt.
func (s *Store) LoadSafeQuestions(ctx context.Context, org int64, versionID bson.ObjectID) ([]StudentQuestion, error) {
	cache := s.cache()
	epoch := cache.epoch()
	key := safeContentCacheKey(org, versionID)
	if raw, found := cache.get(key); found {
		var questions []StudentQuestion
		if json.Unmarshal(raw, &questions) == nil {
			return questions, nil
		}
	}
	version, e := s.LoadContent(ctx, org, versionID)
	if e != nil {
		return nil, e
	}
	questions := SafeQuestions(version.Questions)
	if raw, err := json.Marshal(questions); err == nil {
		cache.put(key, raw, contentTTL, epoch)
	}
	return questions, nil
}
