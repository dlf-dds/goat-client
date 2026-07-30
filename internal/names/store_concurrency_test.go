package names

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// The store sits on the DNS forwarder's hot path with a goroutine per
// query. Concurrent observations must not race (run with -race) and
// must not lose updates.

func TestStoreConcurrentObservations(t *testing.T) {
	t.Parallel()
	st, err := NewStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	now := time.Now()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				name := fmt.Sprintf("host-%d.mesh.goat", g)
				if err := st.RecordObservation(name, fmt.Sprintf("100.64.0.%d", g+1), now); err != nil {
					t.Errorf("RecordObservation: %v", err)
				}
				_ = st.LookupObserved(name, now)
				_ = st.ObservedCount(now)
			}
		}(g)
	}
	wg.Wait()
	// Every goroutine's newest binding must have survived — the lost-update
	// symptom of the unsynchronized read-modify-write.
	for g := 0; g < 8; g++ {
		name := fmt.Sprintf("host-%d.mesh.goat", g)
		rec := st.LookupObserved(name, now)
		if rec == nil {
			t.Errorf("observation for %s lost", name)
		}
	}
}

func TestObservationSameBindingSuppressed(t *testing.T) {
	t.Parallel()
	st, err := NewStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	now := time.Now()
	if err := st.RecordObservation("a.mesh.goat", "100.64.0.9", now); err != nil {
		t.Fatalf("first RecordObservation: %v", err)
	}
	first := st.LookupObserved("a.mesh.goat", now)
	if first == nil {
		t.Fatal("first observation missing")
	}
	// Identical binding within the suppression window: no rewrite, the
	// stored ObservedAt stays put.
	if err := st.RecordObservation("a.mesh.goat", "100.64.0.9", now.Add(10*time.Second)); err != nil {
		t.Fatalf("suppressed RecordObservation: %v", err)
	}
	again := st.LookupObserved("a.mesh.goat", now)
	if again.ObservedAt != first.ObservedAt {
		t.Errorf("suppressed re-observation rewrote the record: %d != %d", again.ObservedAt, first.ObservedAt)
	}
	// A CHANGED binding must always write through.
	if err := st.RecordObservation("a.mesh.goat", "100.64.0.10", now.Add(11*time.Second)); err != nil {
		t.Fatalf("changed-binding RecordObservation: %v", err)
	}
	if rec := st.LookupObserved("a.mesh.goat", now); rec == nil || rec.IP != "100.64.0.10" {
		t.Errorf("changed binding not recorded: %+v", rec)
	}
}
