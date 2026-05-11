package cache

import (
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/stretchr/testify/assert"
)

func newTestMemoStore() *MemoStore {
	return NewMemoStore("", logging.NewConsoleLogger(logging.LevelError))
}

func TestNewMemoStore(t *testing.T) {
	asserts := assert.New(t)

	store := newTestMemoStore()
	asserts.NotNil(store)
	asserts.NotNil(store.Store)
}

func TestMemoStoreSet(t *testing.T) {
	asserts := assert.New(t)

	store := newTestMemoStore()
	err := store.Set("KEY", "vAL", -1)
	asserts.NoError(err)

	val, ok := store.Store.Load("KEY")
	asserts.True(ok)
	asserts.Equal("vAL", val.(itemWithTTL).Value)
}

func TestMemoStoreGet(t *testing.T) {
	asserts := assert.New(t)
	store := newTestMemoStore()

	_ = store.Set("string", "string_val", -1)
	val, ok := store.Get("string")
	asserts.Equal("string_val", val)
	asserts.True(ok)

	val, ok = store.Get("something")
	asserts.Nil(val)
	asserts.False(ok)

	type testStruct struct {
		key int
	}
	test := testStruct{key: 233}
	_ = store.Set("struct", test, -1)
	val, ok = store.Get("struct")
	asserts.True(ok)
	res, ok := val.(testStruct)
	asserts.True(ok)
	asserts.Equal(test, res)

	_ = store.Set("expire", "string_val", 1)
	time.Sleep(2 * time.Second)
	val, ok = store.Get("expire")
	asserts.Nil(val)
	asserts.False(ok)
}

func TestMemoStoreGets(t *testing.T) {
	asserts := assert.New(t)
	store := newTestMemoStore()

	err := store.Set("1", "1,val", -1)
	err = store.Set("2", "2,val", -1)
	err = store.Set("3", "3,val", -1)
	err = store.Set("4", "4,val", -1)
	asserts.NoError(err)

	values, miss := store.Gets([]string{"1", "2", "3", "4"}, "")
	asserts.Len(values, 4)
	asserts.Len(miss, 0)

	values, miss = store.Gets([]string{"1", "2", "9", "10"}, "")
	asserts.Len(values, 2)
	asserts.Equal([]string{"9", "10"}, miss)
}

func TestMemoStoreSets(t *testing.T) {
	asserts := assert.New(t)
	store := newTestMemoStore()

	err := store.Sets(map[string]any{
		"1": "1.val",
		"2": "2.val",
		"3": "3.val",
		"4": "4.val",
	}, "test_")
	asserts.NoError(err)

	vals, miss := store.Gets([]string{"1", "2", "3", "4"}, "test_")
	asserts.Len(miss, 0)
	asserts.Equal(map[string]any{
		"1": "1.val",
		"2": "2.val",
		"3": "3.val",
		"4": "4.val",
	}, vals)
}

func TestMemoStoreDelete(t *testing.T) {
	asserts := assert.New(t)
	store := newTestMemoStore()

	err := store.Sets(map[string]any{
		"1": "1.val",
		"2": "2.val",
		"3": "3.val",
		"4": "4.val",
	}, "test_")
	asserts.NoError(err)

	err = store.Delete("test_", "1", "2")
	asserts.NoError(err)
	values, miss := store.Gets([]string{"1", "2", "3", "4"}, "test_")
	asserts.Equal([]string{"1", "2"}, miss)
	asserts.Equal(map[string]any{"3": "3.val", "4": "4.val"}, values)
}

func TestMemoStoreGarbageCollect(t *testing.T) {
	asserts := assert.New(t)
	store := newTestMemoStore()
	_ = store.Set("test", 1, 1)
	time.Sleep(2 * time.Second)
	store.GarbageCollect(logging.NewConsoleLogger(logging.LevelError))
	_, ok := store.Get("test")
	asserts.False(ok)
}
