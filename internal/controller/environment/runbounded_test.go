package environment

import (
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

func TestRunBoundedRespectsConcurrencyLimit(t *testing.T) {
	const n = 50
	const bound = 4

	reqs := make([]ctrl.Request, 0, n)
	for i := 0; i < n; i++ {
		reqs = append(reqs, ctrl.Request{NamespacedName: types.NamespacedName{Name: "env"}})
	}

	var inFlight, maxInFlight, total int32
	runBounded(reqs, bound, func(ctrl.Request) {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			observed := atomic.LoadInt32(&maxInFlight)
			if cur <= observed || atomic.CompareAndSwapInt32(&maxInFlight, observed, cur) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt32(&total, 1)
		atomic.AddInt32(&inFlight, -1)
	})

	if got := atomic.LoadInt32(&total); got != n {
		t.Fatalf("expected %d invocations, got %d", n, got)
	}
	if got := atomic.LoadInt32(&maxInFlight); got > bound {
		t.Fatalf("max concurrent invocations %d exceeded bound %d", got, bound)
	}
}

func TestRunBoundedClampsNonPositiveBound(t *testing.T) {
	reqs := []ctrl.Request{{NamespacedName: types.NamespacedName{Name: "a"}}, {NamespacedName: types.NamespacedName{Name: "b"}}}
	var total int32
	// A non-positive bound must not deadlock or spawn unbounded work; it is clamped to 1.
	runBounded(reqs, 0, func(ctrl.Request) { atomic.AddInt32(&total, 1) })
	if total != 2 {
		t.Fatalf("expected 2 invocations with clamped bound, got %d", total)
	}
}
