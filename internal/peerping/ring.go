package peerping

import (
	"sync"
	"time"
)

// Sample is one probe outcome. A lost probe (no echo within the timeout)
// carries Lost=true and a zero RTT; a successful probe carries the
// measured round-trip time. At is the wall-clock time the probe
// completed, used to place the point on a time axis.
type Sample struct {
	Seq  uint64
	At   time.Time
	RTT  time.Duration
	Lost bool
}

// Ring is a fixed-capacity rolling history of Samples, oldest evicted
// first. It is the backing store for a peer's latency graph: the Pinger
// appends, the UI/IPC layer reads a snapshot. All methods are safe for
// concurrent use.
type Ring struct {
	mu   sync.Mutex
	buf  []Sample
	head int // index of the next write
	n    int // number of valid samples (≤ cap)
}

// NewRing returns a Ring holding up to capacity samples. A capacity ≤ 0
// falls back to DefaultHistory so a zero-config caller still gets a
// usable window.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = DefaultHistory
	}
	return &Ring{buf: make([]Sample, capacity)}
}

// Add appends a sample, evicting the oldest when the ring is full.
func (r *Ring) Add(s Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.head] = s
	r.head = (r.head + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
}

// Len reports how many samples are currently held.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// Cap reports the ring's fixed capacity.
func (r *Ring) Cap() int { return len(r.buf) }

// Samples returns a copy of the held samples in chronological order
// (oldest first). The copy is safe to read without holding the lock and
// is exactly what a graph plots left-to-right.
func (r *Ring) Samples() []Sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Sample, r.n)
	// The oldest sample sits at head-n (mod cap); walk forward n steps.
	start := (r.head - r.n + len(r.buf)) % len(r.buf)
	for i := 0; i < r.n; i++ {
		out[i] = r.buf[(start+i)%len(r.buf)]
	}
	return out
}

// Last returns the most recent sample and true, or a zero Sample and
// false when the ring is empty.
func (r *Ring) Last() (Sample, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n == 0 {
		return Sample{}, false
	}
	return r.buf[(r.head-1+len(r.buf))%len(r.buf)], true
}

// Stats is a rolling summary over the samples currently in the ring. It
// is honest about an empty or all-lost window: N is the sample count,
// and the latency fields are zero when no successful probe is present.
type Stats struct {
	N       int           // total samples in the window
	Lost    int           // of those, how many were lost
	LossPct float64       // Lost / N as a percentage, 0 when N==0
	Min     time.Duration // over successful samples only
	Max     time.Duration // over successful samples only
	Avg     time.Duration // over successful samples only
	Last    time.Duration // RTT of the most recent successful sample
}

// Stats computes a rolling summary over the current window.
func (r *Ring) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()

	var st Stats
	st.N = r.n
	if r.n == 0 {
		return st
	}

	var sum time.Duration
	var ok int
	start := (r.head - r.n + len(r.buf)) % len(r.buf)
	for i := 0; i < r.n; i++ {
		s := r.buf[(start+i)%len(r.buf)]
		if s.Lost {
			st.Lost++
			continue
		}
		if ok == 0 || s.RTT < st.Min {
			st.Min = s.RTT
		}
		if s.RTT > st.Max {
			st.Max = s.RTT
		}
		sum += s.RTT
		st.Last = s.RTT // walking oldest→newest leaves Last as the newest success
		ok++
	}
	if ok > 0 {
		st.Avg = sum / time.Duration(ok)
	}
	st.LossPct = float64(st.Lost) / float64(st.N) * 100
	return st
}
