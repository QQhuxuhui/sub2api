package erroridentity

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCacheSnapshotsAndFailure(t *testing.T) {
	models := []string{"old"}
	fail := false
	c := NewCache(func(context.Context) ([]string, error) {
		if fail {
			return nil, errors.New("offline")
		}
		return models, nil
	})
	if c.Health().Ready {
		t.Fatal("cold cache ready")
	}
	fail = true
	if c.Refresh(context.Background()) == nil || c.Health().Ready {
		t.Fatal("startup failure hidden")
	}
	fail = false
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := c.Snapshot()
	models = []string{"new"}
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if old.Text("old new", "public") != "public new" {
		t.Fatal("old snapshot changed")
	}
	if c.Snapshot().Text("old new", "public") != "old public" {
		t.Fatal("new snapshot missing")
	}
	fail = true
	if c.Refresh(context.Background()) == nil {
		t.Fatal("failure hidden")
	}
	if !c.Health().Ready || c.Health().Failures != 2 || c.Health().Version != 2 {
		t.Fatal(c.Health())
	}
	if c.Snapshot().Text("new", "public") != "public" {
		t.Fatal("lost last snapshot")
	}
}

func TestCacheNotificationAndShutdown(t *testing.T) {
	calls := make(chan struct{}, 4)
	c := NewCache(func(context.Context) ([]string, error) { calls <- struct{}{}; return []string{"hidden"}, nil })
	c.Start(context.Background(), func(err error) { t.Error(err) })
	defer c.Stop()
	<-calls
	c.Notify()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("committed change did not refresh")
	}
}
