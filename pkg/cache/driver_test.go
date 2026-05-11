package cache

import (
	"testing"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/stretchr/testify/assert"
)

func newTestDriver() Driver {
	return NewMemoStore("", logging.NewConsoleLogger(logging.LevelError))
}

func TestDriverSetGetDelete(t *testing.T) {
	a := assert.New(t)
	driver := newTestDriver()

	a.NoError(driver.Set("123", "321", -1))

	value, ok := driver.Get("123")
	a.True(ok)
	a.Equal("321", value)

	a.NoError(driver.Delete("", "123"))
	_, ok = driver.Get("123")
	a.False(ok)
}

func TestDriverGets(t *testing.T) {
	a := assert.New(t)
	driver := newTestDriver()

	a.NoError(driver.Set("test_1", "1", -1))

	values, missed := driver.Gets([]string{"1", "2"}, "test_")
	a.Equal(map[string]any{"1": "1"}, values)
	a.Equal([]string{"2"}, missed)
}

func TestDriverSets(t *testing.T) {
	a := assert.New(t)
	driver := newTestDriver()

	a.NoError(driver.Sets(map[string]any{"3": "3", "4": "4"}, "test_"))

	value1, ok1 := driver.Get("test_3")
	value2, ok2 := driver.Get("test_4")
	a.True(ok1)
	a.True(ok2)
	a.Equal("3", value1)
	a.Equal("4", value2)
}
