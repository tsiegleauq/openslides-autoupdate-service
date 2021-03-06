package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/openslides/openslides-autoupdate-service/internal/test"
)

func TestCacheGetOrSet(t *testing.T) {
	c := newCache()
	got, err := c.GetOrSet(context.Background(), []string{"key1"}, func([]string) (map[string]json.RawMessage, error) {
		return map[string]json.RawMessage{
			"key1": json.RawMessage("value"),
		}, nil
	})

	if err != nil {
		t.Errorf("GetOrSet() returned the unexpected error: %v", err)
	}
	expect := []string{"value"}
	if len(got) != 1 || string(got[0]) != expect[0] {
		t.Errorf("GetOrSet() returned `%v`, expected `%v`", got, expect)
	}
}

func TestCacheGetOrSetMissingKeys(t *testing.T) {
	c := newCache()
	got, err := c.GetOrSet(context.Background(), []string{"key1", "key2"}, func([]string) (map[string]json.RawMessage, error) {
		return map[string]json.RawMessage{
			"key1": json.RawMessage("value"),
		}, nil
	})

	if err != nil {
		t.Errorf("GetOrSet() returned the unexpected error: %v", err)
	}
	expect := []json.RawMessage{[]byte("value"), nil}
	if !test.CmpSliceBytes(got, expect) {
		t.Errorf("GetOrSet() returned `%s`, expected `%s`", got, expect)
	}
}

func TestCacheGetOrSetNoSecondCall(t *testing.T) {
	c := newCache()
	c.GetOrSet(context.Background(), []string{"key1"}, func([]string) (map[string]json.RawMessage, error) {
		return map[string]json.RawMessage{"key1": json.RawMessage("value")}, nil
	})

	var called bool

	got, err := c.GetOrSet(context.Background(), []string{"key1"}, func([]string) (map[string]json.RawMessage, error) {
		called = true
		return map[string]json.RawMessage{"key1": json.RawMessage("Shut not be returned")}, nil
	})

	if err != nil {
		t.Errorf("GetOrSet() returned the unexpected error %v", err)
	}
	expect := []string{"value"}
	if len(got) != 1 || string(got[0]) != expect[0] {
		t.Errorf("GetOrSet() returned %v, expected %v", got, expect)
	}
	if called {
		t.Errorf("GetOrSet() called the set method")
	}
}

func TestCacheGetOrSetBlockSecondCall(t *testing.T) {
	c := newCache()
	wait := make(chan struct{})
	go func() {
		c.GetOrSet(context.Background(), []string{"key1"}, func([]string) (map[string]json.RawMessage, error) {
			<-wait
			return map[string]json.RawMessage{"key1": json.RawMessage("value")}, nil
		})
	}()

	// close done, when the second call is finished.
	done := make(chan struct{})
	go func() {
		c.GetOrSet(context.Background(), []string{"key1"}, func([]string) (map[string]json.RawMessage, error) {
			return map[string]json.RawMessage{"key1": json.RawMessage("Shut not be returned")}, nil
		})
		close(done)
	}()

	select {
	case <-done:
		t.Errorf("done channel already closed")
	default:
	}

	close(wait)

	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Errorf("Second GetOrSet-Call was not done one Millisecond after the frist GetOrSet-Call was called.")
	}
}

func TestCacheSetIfExist(t *testing.T) {
	c := newCache()
	c.GetOrSet(context.Background(), []string{"key1"}, func([]string) (map[string]json.RawMessage, error) {
		return map[string]json.RawMessage{"key1": json.RawMessage("value")}, nil
	})

	// Set key1 and key2. key1 is in the cache. key2 should be ignored.
	c.SetIfExist(map[string]json.RawMessage{
		"key1": json.RawMessage("new_value"),
		"key2": json.RawMessage("new_value"),
	})

	// Get key1 and key2 from the cache. The existing key1 should not be set.
	// key2 should be.
	got, _ := c.GetOrSet(context.Background(), []string{"key1", "key2"}, func(keys []string) (map[string]json.RawMessage, error) {
		data := make(map[string]json.RawMessage)
		for _, key := range keys {
			data[key] = json.RawMessage(key)
		}
		return data, nil
	})

	expect := []string{"new_value", "key2"}
	if len(got) != 2 || string(got[0]) != expect[0] || string(got[1]) != expect[1] {
		t.Errorf("Got %v, expected %v", got, expect)
	}
}

func TestCacheSetIfExistParallelToGetOrSet(t *testing.T) {
	c := newCache()

	waitForGetOrSet := make(chan struct{})
	go func() {
		c.GetOrSet(context.Background(), []string{"key1"}, func(keys []string) (map[string]json.RawMessage, error) {
			// Signal, that GetOrSet was called.
			close(waitForGetOrSet)

			// Wait for some time.
			time.Sleep(10 * time.Millisecond)
			return map[string]json.RawMessage{"key1": json.RawMessage("shut not be used")}, nil
		})
	}()

	<-waitForGetOrSet

	// Set key1 to new value and stop the ongoing GetOrSet-Call
	c.SetIfExist(map[string]json.RawMessage{"key1": json.RawMessage("new value")})

	got, _ := c.GetOrSet(context.Background(), []string{"key1"}, func([]string) (map[string]json.RawMessage, error) {
		return map[string]json.RawMessage{"key1": json.RawMessage("Expect values in cache")}, nil
	})

	expect := []string{"new value"}
	if len(got) != 1 || string(got[0]) != expect[0] {
		t.Errorf("Got `%s`, expected `%s`", got, expect)
	}
}

func TestCacheGetOrSetOldData(t *testing.T) {
	// GetOrSet is called with key1. It returns key1 and key2 on version1 but
	// takes a long time. In the meantime there is an update via setIfExist for
	// key1 and key2 on version2. At the end, there should not be the old
	// version1 in the cache (version2 or 'does not exist' is ok).
	c := newCache()

	waitForGetOrSetStart := make(chan struct{})
	waitForGetOrSetEnd := make(chan struct{})
	waitForSetIfExist := make(chan struct{})

	go func() {
		c.GetOrSet(context.Background(), []string{"key1"}, func(keys []string) (map[string]json.RawMessage, error) {
			close(waitForGetOrSetStart)
			data := map[string]json.RawMessage{
				"key1": []byte("v1"),
				"key2": []byte("v1"),
			}
			<-waitForSetIfExist
			return data, nil
		})
		close(waitForGetOrSetEnd)
	}()

	<-waitForGetOrSetStart
	c.SetIfExist(map[string]json.RawMessage{
		"key1": []byte("v2"),
		"key2": []byte("v2"),
	})
	close(waitForSetIfExist)

	<-waitForGetOrSetEnd
	data, err := c.GetOrSet(context.Background(), []string{"key1", "key2"}, func(keys []string) (map[string]json.RawMessage, error) {
		data := make(map[string]json.RawMessage)
		for _, key := range keys {
			data[key] = []byte("key not in cache")
		}
		return data, nil
	})
	if err != nil {
		t.Errorf("GetOrSet returned unexpected error: %v", err)
	}

	if string(data[0]) != "v2" {
		t.Errorf("value for key1 is %s, expected `v2`", data[0])
	}

	if string(data[1]) == "v1" {
		t.Errorf("value for key2 is `v1`, expected `v2` or `key not in cache`")
	}
}

func TestCacheErrorOnFetching(t *testing.T) {
	// Make sure, that if a GetOrSet call fails the requested keys are not left
	// in pending state.
	c := newCache()
	rErr := errors.New("GetOrSet Error")
	_, err := c.GetOrSet(context.Background(), []string{"key1"}, func(keys []string) (map[string]json.RawMessage, error) {
		return nil, rErr
	})

	if !errors.Is(err, rErr) {
		t.Errorf("GetOrSet returned err `%v`, expected `%v`", err, rErr)
	}

	done := make(chan struct{})
	go func() {
		_, err := c.GetOrSet(context.Background(), []string{"key1"}, func(keys []string) (map[string]json.RawMessage, error) {
			return map[string]json.RawMessage{
				"key1": []byte("value"),
			}, nil
		})
		if err != nil {
			t.Errorf("Second GetOrSet returned unexpected err: %v", err)
		}
		close(done)
	}()

	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Errorf("Second GetOrSet-Call was not done one Millisecond")
	}
}

func TestCacheFailInOthetGetOrSetCall(t *testing.T) {
	// When two GetOrSetCalls are run in parallel and the first one returns an
	// error, then the second one should retry the fetch the key.
	c := newCache()

	waitForFirstGetOrSetStart := make(chan struct{})

	go func() {
		c.GetOrSet(context.Background(), []string{"key"}, func(keys []string) (map[string]json.RawMessage, error) {
			close(waitForFirstGetOrSetStart)

			// Wait a shot time so the second call to getOrSet can start.
			time.Sleep(time.Millisecond)
			return nil, errors.New("Some error")
		})
	}()

	<-waitForFirstGetOrSetStart
	data, err := c.GetOrSet(context.Background(), []string{"key"}, func(keys []string) (map[string]json.RawMessage, error) {
		return map[string]json.RawMessage{
			"key": []byte("value"),
		}, nil
	})
	if err != nil {
		t.Fatalf("second GetOrSet returned unexpected err: %v", err)
	}

	if string(data[0]) != "value" {
		t.Errorf("second GetOrSet returned `%v`, expected `value`", data[0])
	}
}
