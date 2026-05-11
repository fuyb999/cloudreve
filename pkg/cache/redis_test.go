package cache

import (
	"errors"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/gomodule/redigo/redis"
	"github.com/rafaeljusto/redigomock"
	"github.com/stretchr/testify/assert"
)

func newRedisTestStore(conn redis.Conn) *RedisStore {
	return &RedisStore{
		pool: &redis.Pool{
			Dial:    func() (redis.Conn, error) { return conn, nil },
			MaxIdle: 10,
		},
	}
}

func TestNewRedisStore(t *testing.T) {
	asserts := assert.New(t)

	store := NewRedisStore(
		logging.NewConsoleLogger(logging.LevelError),
		10,
		&conf.Redis{Network: "tcp", Server: "", DB: "0"},
	)
	asserts.NotNil(store)

	testConn := redigomock.NewConn()
	cmd := testConn.Command("PING").Expect("PONG")
	err := store.pool.TestOnBorrow(testConn, time.Now())
	asserts.NoError(err)
	asserts.Equal(1, testConn.Stats(cmd))
}

func TestRedisStoreSet(t *testing.T) {
	asserts := assert.New(t)
	conn := redigomock.NewConn()
	store := newRedisTestStore(conn)

	cmd := conn.Command("SET", "test", redigomock.NewAnyData()).ExpectStringSlice("OK")
	err := store.Set("test", "test val", -1)
	asserts.NoError(err)
	asserts.Equal(1, conn.Stats(cmd))

	conn.Clear()
	cmd = conn.Command("SETEX", "test", 10, redigomock.NewAnyData()).ExpectStringSlice("OK")
	err = store.Set("test", "test val", 10)
	asserts.NoError(err)
	asserts.Equal(1, conn.Stats(cmd))

	conn.Clear()
	cmd = conn.Command("SET", "test", redigomock.NewAnyData()).ExpectError(errors.New("error"))
	err = store.Set("test", "test val", -1)
	asserts.Error(err)
	asserts.Equal(1, conn.Stats(cmd))

	store.pool = &redis.Pool{
		Dial:    func() (redis.Conn, error) { return nil, errors.New("error") },
		MaxIdle: 10,
	}
	err = store.Set("test", "123", -1)
	asserts.Error(err)
}

func TestRedisStoreGet(t *testing.T) {
	asserts := assert.New(t)
	conn := redigomock.NewConn()
	store := newRedisTestStore(conn)

	expectVal, _ := serializer("test val")
	cmd := conn.Command("GET", "test").Expect(expectVal)
	val, ok := store.Get("test")
	asserts.Equal(1, conn.Stats(cmd))
	asserts.True(ok)
	asserts.Equal("test val", val.(string))

	conn.Clear()
	cmd = conn.Command("GET", "test").Expect(nil)
	val, ok = store.Get("test")
	asserts.Equal(1, conn.Stats(cmd))
	asserts.False(ok)
	asserts.Nil(val)

	conn.Clear()
	cmd = conn.Command("GET", "test").Expect([]byte{0x20})
	val, ok = store.Get("test")
	asserts.Equal(1, conn.Stats(cmd))
	asserts.False(ok)
	asserts.Nil(val)

	store.pool = &redis.Pool{
		Dial:    func() (redis.Conn, error) { return nil, errors.New("error") },
		MaxIdle: 10,
	}
	val, ok = store.Get("test")
	asserts.False(ok)
	asserts.Nil(val)
}

func TestRedisStoreGets(t *testing.T) {
	asserts := assert.New(t)
	conn := redigomock.NewConn()
	store := newRedisTestStore(conn)

	value1, _ := serializer("1")
	value2, _ := serializer("2")
	cmd := conn.Command("MGET", "test_1", "test_2").ExpectSlice(value1, value2)
	res, missed := store.Gets([]string{"1", "2"}, "test_")
	asserts.Equal(1, conn.Stats(cmd))
	asserts.Len(missed, 0)
	asserts.Equal("1", res["1"].(string))
	asserts.Equal("2", res["2"].(string))

	conn.Clear()
	cmd = conn.Command("MGET", "test_1", "test_2").ExpectSlice(nil, value2)
	res, missed = store.Gets([]string{"1", "2"}, "test_")
	asserts.Equal(1, conn.Stats(cmd))
	asserts.Equal([]string{"1"}, missed)
	asserts.Equal("2", res["2"].(string))

	conn.Clear()
	cmd = conn.Command("MGET", "test_1", "test_2").ExpectError(errors.New("error"))
	res, missed = store.Gets([]string{"1", "2"}, "test_")
	asserts.Equal(1, conn.Stats(cmd))
	asserts.Empty(res)
	asserts.Equal([]string{"1", "2"}, missed)

	store.pool = &redis.Pool{
		Dial:    func() (redis.Conn, error) { return nil, errors.New("error") },
		MaxIdle: 10,
	}
	res, missed = store.Gets([]string{"1", "2"}, "test_")
	asserts.Empty(res)
	asserts.Equal([]string{"1", "2"}, missed)
}

func TestRedisStoreSets(t *testing.T) {
	asserts := assert.New(t)
	conn := redigomock.NewConn()
	store := newRedisTestStore(conn)

	cmd := conn.Command("MSET", redigomock.NewAnyData(), redigomock.NewAnyData(), redigomock.NewAnyData(), redigomock.NewAnyData()).ExpectStringSlice("OK")
	err := store.Sets(map[string]any{"1": "1", "2": "2"}, "test_")
	asserts.NoError(err)
	asserts.Equal(1, conn.Stats(cmd))

	conn.Clear()
	cmd = conn.Command("MSET", redigomock.NewAnyData(), redigomock.NewAnyData(), redigomock.NewAnyData(), redigomock.NewAnyData()).ExpectError(errors.New("error"))
	err = store.Sets(map[string]any{"1": "1", "2": "2"}, "test_")
	asserts.Error(err)
	asserts.Equal(1, conn.Stats(cmd))

	store.pool = &redis.Pool{
		Dial:    func() (redis.Conn, error) { return nil, errors.New("error") },
		MaxIdle: 10,
	}
	err = store.Sets(map[string]any{"1": "1", "2": "2"}, "test_")
	asserts.Error(err)
}

func TestRedisStoreDelete(t *testing.T) {
	asserts := assert.New(t)
	conn := redigomock.NewConn()
	store := newRedisTestStore(conn)

	cmd := conn.Command("DEL", redigomock.NewAnyData(), redigomock.NewAnyData(), redigomock.NewAnyData(), redigomock.NewAnyData()).ExpectStringSlice("OK")
	err := store.Delete("test_", "1", "2", "3", "4")
	asserts.NoError(err)
	asserts.Equal(1, conn.Stats(cmd))

	conn.Clear()
	cmd = conn.Command("DEL", redigomock.NewAnyData(), redigomock.NewAnyData(), redigomock.NewAnyData(), redigomock.NewAnyData()).ExpectError(errors.New("error"))
	err = store.Delete("test_", "1", "2", "3", "4")
	asserts.Error(err)
	asserts.Equal(1, conn.Stats(cmd))

	store.pool = &redis.Pool{
		Dial:    func() (redis.Conn, error) { return nil, errors.New("error") },
		MaxIdle: 10,
	}
	err = store.Delete("test_", "1", "2", "3", "4")
	asserts.Error(err)
}
