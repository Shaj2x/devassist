// Package lock implements a Redis-backed distributed lock.
//
// Acquire uses SET key token NX PX ttl, so exactly one holder wins. Release
// and extend run Lua scripts that act only if the stored token is still ours,
// so a holder whose lock already expired can never delete or extend a lock
// that someone else now holds. KeepAlive extends the TTL in the background
// for work that may outlive it (indexing a big repo, running a sandbox).
package lock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotAcquired means another holder owns the lock.
var ErrNotAcquired = errors.New("lock held by another worker")

// KeyPrefix namespaces every DevAssist lock.
const KeyPrefix = "devassist:lock:"

var (
	releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0`)
	extendScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0`)
)

// Lock is a held lock. It is not safe to share between goroutines except
// through KeepAlive, which it starts itself.
type Lock struct {
	client redis.Scripter
	key    string
	token  string
	ttl    time.Duration
	stop   context.CancelFunc
	done   chan struct{}
}

// Client is the subset of the Redis client the lock needs.
type Client interface {
	redis.Scripter
	SetNX(ctx context.Context, key string, value any, expiration time.Duration) *redis.BoolCmd
}

// Acquire tries once to take the lock named name for ttl.
func Acquire(ctx context.Context, client Client, name string, ttl time.Duration) (*Lock, error) {
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	key := KeyPrefix + name
	ok, err := client.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotAcquired
	}
	return &Lock{client: client, key: key, token: token, ttl: ttl}, nil
}

// KeepAlive extends the lock every ttl/3 until Release is called or ctx ends.
func (l *Lock) KeepAlive(ctx context.Context) {
	ctx, l.stop = context.WithCancel(ctx)
	l.done = make(chan struct{})
	go func() {
		defer close(l.done)
		ticker := time.NewTicker(l.ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// If this fails the lock may expire; the next extend (or the
				// holder's own idempotent writes) handles it.
				_, _ = extendScript.Run(ctx, l.client, []string{l.key}, l.token, l.ttl.Milliseconds()).Result()
			}
		}
	}()
}

// Release stops KeepAlive and deletes the lock if we still hold it.
func (l *Lock) Release(ctx context.Context) error {
	if l.stop != nil {
		l.stop()
		<-l.done
	}
	_, err := releaseScript.Run(ctx, l.client, []string{l.key}, l.token).Result()
	return err
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
