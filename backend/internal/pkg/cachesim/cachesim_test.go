package cachesim

import (
	"context"
	"testing"
)

func TestDecideOnceAndApplyIdempotent(t *testing.T) {
	s := New()
	calls := 0
	got := s.Decide(10000, 2000, func(total, cached int) int { calls++; return 8000 })
	if got != 8000 || calls != 1 {
		t.Fatalf("first decide: got %d calls %d", got, calls)
	}
	if again := s.Decide(10000, 2000, func(int, int) int { calls++; return 1 }); again != 8000 || calls != 1 {
		t.Fatalf("second decide must reuse: got %d calls %d", again, calls)
	}
	if v, changed := s.Apply(10000, 2000); v != 8000 || !changed {
		t.Fatalf("apply raw: %d %v", v, changed)
	}
	if v, changed := s.Apply(10000, 8000); v != 8000 || changed {
		t.Fatalf("apply already simulated: %d %v", v, changed)
	}
	if v, changed := s.Apply(9000, 2000); v != 2000 || changed {
		t.Fatalf("apply mismatched total: %d %v", v, changed)
	}
	if !s.IsApplied(10000, 8000) || s.IsApplied(10000, 2000) {
		t.Fatalf("IsApplied wrong")
	}
}

func TestDecideClamps(t *testing.T) {
	s := New()
	if got := s.Decide(5000, 3000, func(int, int) int { return 100 }); got != 3000 {
		t.Fatalf("must not go below raw cached: %d", got)
	}
	s2 := New()
	if got := s2.Decide(5000, 3000, func(int, int) int { return 99999 }); got != 5000 {
		t.Fatalf("must not exceed total: %d", got)
	}
}

func TestNilSafe(t *testing.T) {
	var s *State
	if got := s.Decide(1, 1, nil); got != 1 {
		t.Fatal("nil decide")
	}
	if v, changed := s.Apply(1, 1); v != 1 || changed {
		t.Fatal("nil apply")
	}
	if FromContext(context.Background()) != nil {
		t.Fatal("empty ctx")
	}
	st := New()
	if FromContext(WithState(context.Background(), st)) != st {
		t.Fatal("roundtrip")
	}
}
