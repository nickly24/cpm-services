package bff

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// All keys for one session share a Redis Cluster hash tag. Only authentication
// changes acquire a writer; regular requests hold independent reader leases.
// A queued writer blocks new readers, preserving cabinet switching semantics.
type redisSessionStore struct{ client *redis.Client }

func newRedisSessionStore(address, password string) *redisSessionStore {
	return &redisSessionStore{client: redis.NewClient(&redis.Options{
		Addr: address, Password: password, Protocol: 2, PoolSize: 50,
		DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second,
		MaxRetries: -1, DisableIdentity: true,
	})}
}

func sessionKeys(id string) []string {
	base := "cpm:bff:{" + id + "}:"
	return []string{base + "data", base + "readers", base + "writer"}
}

func (s *redisSessionStore) Create(ctx context.Context, id string, v *session) error {
	raw, err := encodeSession(v)
	if err != nil {
		return err
	}
	ttl := time.Until(v.until)
	if ttl <= 0 {
		return errSessionMissing
	}
	ok, err := s.client.SetNX(ctx, sessionKeys(id)[0], raw, ttl).Result()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("session already exists")
	}
	return nil
}

func (s *redisSessionStore) Load(ctx context.Context, id string) (*session, error) {
	raw, err := s.client.Get(ctx, sessionKeys(id)[0]).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, errSessionMissing
	}
	if err != nil {
		return nil, err
	}
	return decodeSession(raw)
}

var acquireSessionScript = redis.NewScript(`
local clock = redis.call('TIME')
local now = clock[1] * 1000 + math.floor(clock[2] / 1000)
local raw = redis.call('GET', KEYS[1])
if not raw then return {0} end
local writer = redis.call('GET', KEYS[3])
if ARGV[1] == 'w' then
  if writer and writer ~= ARGV[2] then return {1} end
  if not writer then redis.call('SET', KEYS[3], ARGV[2], 'PX', ARGV[3]) end
  redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now)
  if redis.call('ZCARD', KEYS[2]) ~= 0 then return {1} end
else
  if writer then return {1} end
  redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now)
  redis.call('ZADD', KEYS[2], now + tonumber(ARGV[3]), ARGV[2])
  redis.call('PEXPIRE', KEYS[2], ARGV[3])
end
return {2, raw}
`)

var releaseSessionScript = redis.NewScript(`
if ARGV[1] == 'w' then
  if redis.call('GET', KEYS[3]) == ARGV[2] then redis.call('DEL', KEYS[3]) end
else
  redis.call('ZREM', KEYS[2], ARGV[2])
  if redis.call('ZCARD', KEYS[2]) == 0 then redis.call('DEL', KEYS[2]) end
end
return 1
`)

var saveSessionScript = redis.NewScript(`
if redis.call('GET', KEYS[3]) ~= ARGV[1] then return 0 end
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
return 1
`)

var deleteSessionScript = redis.NewScript(`
if ARGV[1] == 'w' then
  if redis.call('GET', KEYS[3]) ~= ARGV[2] then return 0 end
else
  local clock = redis.call('TIME')
  local now = clock[1] * 1000 + math.floor(clock[2] / 1000)
  local until_at = redis.call('ZSCORE', KEYS[2], ARGV[2])
  if not until_at or tonumber(until_at) <= now then return 0 end
end
redis.call('DEL', KEYS[1])
return 1
`)

func (s *redisSessionStore) Acquire(ctx context.Context, id string, exclusive bool) (*sessionLease, error) {
	keys, token, mode := sessionKeys(id), random(), "r"
	if exclusive {
		mode = "w"
	}
	release := func() {
		// The request may have been canceled; bounded independent cleanup releases
		// its lease immediately rather than making another tab wait for expiry.
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := releaseSessionScript.Run(c, s.client, keys, mode, token).Err(); err != nil {
			log.Print("BFF session lease cleanup unavailable")
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		result, err := acquireSessionScript.Run(ctx, s.client, keys, mode, token, sessionLeaseDuration.Milliseconds()).Slice()
		if err != nil {
			release()
			return nil, err
		}
		if len(result) == 0 {
			release()
			return nil, errors.New("invalid session lease response")
		}
		switch result[0] {
		case int64(0):
			release()
			return nil, errSessionMissing
		case int64(2):
			if len(result) != 2 {
				release()
				return nil, errors.New("invalid session lease response")
			}
			raw, ok := result[1].(string)
			if !ok {
				release()
				return nil, errors.New("invalid stored session")
			}
			v, err := decodeSession([]byte(raw))
			if err != nil {
				release()
				return nil, err
			}
			l := &sessionLease{value: v, release: release}
			l.save = func(c context.Context, next *session) error {
				if !exclusive {
					return errSessionLeaseLost
				}
				raw, err := encodeSession(next)
				if err != nil {
					return err
				}
				ttl := time.Until(next.until).Milliseconds()
				if ttl <= 0 {
					return errSessionMissing
				}
				n, err := saveSessionScript.Run(c, s.client, keys, token, raw, ttl).Int()
				if err != nil {
					return err
				}
				if n != 1 {
					return errSessionLeaseLost
				}
				return nil
			}
			l.delete = func(c context.Context) error {
				n, err := deleteSessionScript.Run(c, s.client, keys, mode, token).Int()
				if err != nil {
					return err
				}
				if n != 1 {
					return errSessionLeaseLost
				}
				return nil
			}
			return l, nil
		case int64(1):
			select {
			case <-ctx.Done():
				release()
				return nil, ctx.Err()
			case <-time.After(15 * time.Millisecond):
			}
		default:
			release()
			return nil, fmt.Errorf("invalid session lease status")
		}
	}
}

func (s *redisSessionStore) Ready(ctx context.Context) error { return s.client.Ping(ctx).Err() }
func (s *redisSessionStore) Close() error                    { return s.client.Close() }
