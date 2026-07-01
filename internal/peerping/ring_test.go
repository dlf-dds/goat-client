package peerping

import (
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestRingDefaultsAndCap(t *testing.T) {
	if got := NewRing(0).Cap(); got != DefaultHistory {
		t.Fatalf("NewRing(0).Cap() = %d, want DefaultHistory %d", got, DefaultHistory)
	}
	if got := NewRing(-5).Cap(); got != DefaultHistory {
		t.Fatalf("NewRing(-5).Cap() = %d, want DefaultHistory %d", got, DefaultHistory)
	}
	if got := NewRing(7).Cap(); got != 7 {
		t.Fatalf("NewRing(7).Cap() = %d, want 7", got)
	}
}

func TestRingEmpty(t *testing.T) {
	r := NewRing(4)
	if r.Len() != 0 {
		t.Fatalf("empty ring Len() = %d, want 0", r.Len())
	}
	if s := r.Samples(); len(s) != 0 {
		t.Fatalf("empty ring Samples() len = %d, want 0", len(s))
	}
	if _, ok := r.Last(); ok {
		t.Fatalf("empty ring Last() ok = true, want false")
	}
	st := r.Stats()
	if st.N != 0 || st.Lost != 0 || st.Avg != 0 || st.LossPct != 0 {
		t.Fatalf("empty ring Stats() = %+v, want zero", st)
	}
}

func TestRingChronologicalOrderAndEviction(t *testing.T) {
	r := NewRing(3)
	for i := 1; i <= 5; i++ {
		r.Add(Sample{Seq: uint64(i), RTT: ms(i)})
	}
	// Capacity 3 keeps the last three: seq 3,4,5 oldest→newest.
	got := r.Samples()
	if len(got) != 3 {
		t.Fatalf("Samples() len = %d, want 3", len(got))
	}
	wantSeq := []uint64{3, 4, 5}
	for i, s := range got {
		if s.Seq != wantSeq[i] {
			t.Fatalf("Samples()[%d].Seq = %d, want %d (order: %+v)", i, s.Seq, wantSeq[i], got)
		}
	}
	if last, ok := r.Last(); !ok || last.Seq != 5 {
		t.Fatalf("Last() = %+v,%v, want seq 5,true", last, ok)
	}
	if r.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", r.Len())
	}
}

func TestRingStatsMixed(t *testing.T) {
	r := NewRing(10)
	r.Add(Sample{Seq: 1, RTT: ms(10)})
	r.Add(Sample{Seq: 2, RTT: ms(30)})
	r.Add(Sample{Seq: 3, Lost: true})
	r.Add(Sample{Seq: 4, RTT: ms(20)})

	st := r.Stats()
	if st.N != 4 {
		t.Fatalf("N = %d, want 4", st.N)
	}
	if st.Lost != 1 {
		t.Fatalf("Lost = %d, want 1", st.Lost)
	}
	if st.Min != ms(10) {
		t.Fatalf("Min = %v, want 10ms", st.Min)
	}
	if st.Max != ms(30) {
		t.Fatalf("Max = %v, want 30ms", st.Max)
	}
	if st.Avg != ms(20) { // (10+30+20)/3
		t.Fatalf("Avg = %v, want 20ms", st.Avg)
	}
	if st.Last != ms(20) { // newest successful sample
		t.Fatalf("Last = %v, want 20ms", st.Last)
	}
	if st.LossPct != 25 {
		t.Fatalf("LossPct = %v, want 25", st.LossPct)
	}
}

func TestRingStatsAllLost(t *testing.T) {
	r := NewRing(4)
	for i := 1; i <= 3; i++ {
		r.Add(Sample{Seq: uint64(i), Lost: true})
	}
	st := r.Stats()
	if st.N != 3 || st.Lost != 3 {
		t.Fatalf("N,Lost = %d,%d, want 3,3", st.N, st.Lost)
	}
	if st.Min != 0 || st.Max != 0 || st.Avg != 0 || st.Last != 0 {
		t.Fatalf("all-lost latency fields = min%v max%v avg%v last%v, want all 0", st.Min, st.Max, st.Avg, st.Last)
	}
	if st.LossPct != 100 {
		t.Fatalf("LossPct = %v, want 100", st.LossPct)
	}
}
