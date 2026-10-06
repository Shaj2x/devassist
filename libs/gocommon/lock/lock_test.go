package lock

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newClient(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	return mr, redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func TestOnlyOneHolder(t *testing.T) {
	_, rdb := newClient(t)
	ctx := context.Background()

	first, err := Acquire(ctx, rdb, "index:repo-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(ctx, rdb, "index:repo-1", time.Minute); !errors.Is(err, ErrNotAcquired) {
		t.Fatalf("second acquire: got %v, want ErrNotAcquired", err)
	}
	if _, err := Acquire(ctx, rdb, "index:repo-2", time.Minute); err != nil {
		t.Fatalf("different key should be free: %v", err)
	}

	if err := first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(ctx, rdb, "index:repo-1", time.Minute); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

func TestReleaseDoesNotDeleteSomeoneElsesLock(t *testing.T) {
	mr, rdb := newClient(t)
	ctx := context.Background()

	stale, err := Acquire(ctx, rdb, "job:1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	mr.FastForward(2 * time.Second) // stale holder's lock expires
	fresh, err := Acquire(ctx, rdb, "job:1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	if err := stale.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := mr.Get(KeyPrefix + "job:1"); got != fresh.token {
		t.Fatalf("stale release removed the new holder's lock")
	}
}

func TestKeepAliveExtendsTTL(t *testing.T) {
	mr, rdb := newClient(t)
	ctx := context.Background()

	l, err := Acquire(ctx, rdb, "long", 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	mr.SetTTL(KeyPrefix+"long", 50*time.Millisecond)
	l.KeepAlive(ctx)
	time.Sleep(250 * time.Millisecond) // > one tick of ttl/3
	if ttl := mr.TTL(KeyPrefix + "long"); ttl < 100*time.Millisecond {
		t.Fatalf("ttl not extended: %v", ttl)
	}
	if err := l.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if mr.Exists(KeyPrefix + "long") {
		t.Fatal("lock still present after release")
	}
}
