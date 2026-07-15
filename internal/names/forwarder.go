package names

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

// Forwarder is the device-wide fallback resolver: a local UDP DNS server
// that answers A/AAAA queries live-upstream-first (never shadowed), then
// from the verified snapshot, then from the noncanonical observed tier —
// every fallback answer logged with its provenance label.
//
// It binds loopback only. Pointing the OS at it (scutil match-domains on
// macOS, resolved routing domains on Linux) is the tunnel/dns adapter's
// job and is wired per-mode by the daemon; the forwarder itself is
// mode-agnostic.
type Forwarder struct {
	store     *Store
	upstreams func() []string // e.g. ["100.64.165.203:5353"]; re-read per query
	timeout   time.Duration

	mu       sync.Mutex
	server   *dns.Server
	addr     string
	lastFall atomic.Int64 // unix seconds of the last fallback-served answer
	served   atomic.Int64 // total fallback answers served
}

// NewForwarder builds a forwarder over the given store. upstreams is
// consulted per query so the daemon can swap mesh nameservers at
// runtime (e.g. after re-enrollment) without a restart.
func NewForwarder(store *Store, upstreams func() []string, timeout time.Duration) *Forwarder {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Forwarder{store: store, upstreams: upstreams, timeout: timeout}
}

// Start binds the UDP listener (addr like "127.0.0.1:53535") and serves
// until ctx is cancelled. Returns the bound address (useful with :0).
func (f *Forwarder) Start(ctx context.Context, addr string) (string, error) {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return "", fmt.Errorf("names forwarder bind %s: %w", addr, err)
	}
	srv := &dns.Server{PacketConn: conn, Handler: dns.HandlerFunc(f.handle)}
	f.mu.Lock()
	f.server = srv
	f.addr = conn.LocalAddr().String()
	f.mu.Unlock()
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown()
	}()
	go func() {
		if err := srv.ActivateAndServe(); err != nil && ctx.Err() == nil {
			log.Printf("names forwarder: serve ended: %v", err)
		}
	}()
	return f.addr, nil
}

// Addr returns the bound address ("" before Start).
func (f *Forwarder) Addr() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addr
}

// FallbackStats reports (total fallback answers, time of the last one).
func (f *Forwarder) FallbackStats() (int64, time.Time) {
	ts := f.lastFall.Load()
	var t time.Time
	if ts > 0 {
		t = time.Unix(ts, 0)
	}
	return f.served.Load(), t
}

func (f *Forwarder) handle(w dns.ResponseWriter, req *dns.Msg) {
	if len(req.Question) == 0 {
		reply := new(dns.Msg)
		reply.SetRcode(req, dns.RcodeFormatError)
		_ = w.WriteMsg(reply)
		return
	}
	q := req.Question[0]

	// 1. Live upstream — never shadowed. Any definitive upstream answer
	// (including NXDOMAIN) is relayed verbatim; only no-answer-at-all
	// (dead upstreams, timeouts) falls through.
	if resp := f.tryUpstreams(req); resp != nil {
		if resp.Rcode == dns.RcodeSuccess && len(resp.Answer) > 0 {
			f.recordLiveAnswers(resp)
			_ = w.WriteMsg(resp)
			return
		}
		if resp.Rcode != dns.RcodeSuccess && resp.Rcode != dns.RcodeServerFailure {
			_ = w.WriteMsg(resp)
			return
		}
		// SERVFAIL or empty success → treat as name-plane failure and
		// consult the fallback tiers below.
	}

	// 2/3. Signed snapshot, then observed tier — A/AAAA only.
	if q.Qtype != dns.TypeA && q.Qtype != dns.TypeAAAA {
		reply := new(dns.Msg)
		reply.SetRcode(req, dns.RcodeServerFailure)
		_ = w.WriteMsg(reply)
		return
	}
	now := time.Now()
	snap, snapErr := f.store.LoadSnapshot()
	obs := f.store.LookupObserved(q.Name, now)
	ans, err := PickFallback(snap, snapErr, obs, q.Name, now)
	if err != nil {
		log.Printf("names forwarder: %s unresolvable (live upstreams unreachable; %v)", q.Name, err)
		reply := new(dns.Msg)
		reply.SetRcode(req, dns.RcodeServerFailure)
		_ = w.WriteMsg(reply)
		return
	}
	wantV6 := q.Qtype == dns.TypeAAAA
	if ans.IP.Is6() != wantV6 {
		// Record family doesn't match the question: answer empty success
		// so the client retries the other family rather than erroring.
		reply := new(dns.Msg)
		reply.SetReply(req)
		_ = w.WriteMsg(reply)
		return
	}
	switch ans.Source {
	case SourceSnapshot:
		label := fmt.Sprintf("signed snapshot serial %d, age %s", ans.Meta.Serial, AgeHuman(ans.Meta.Age))
		if ans.Meta.Grade == GradeAging {
			label += " — STALE (aging)"
		}
		log.Printf("names forwarder: %s → %s via %s (mesh DNS unreachable)", q.Name, ans.IP, label)
	case SourceObserved:
		log.Printf("names forwarder: %s → %s via NONCANONICAL (ad hoc) observed record, age %s (mesh DNS unreachable; codify this record)",
			q.Name, ans.IP, AgeHuman(ans.ObservedAge))
	}
	f.served.Add(1)
	f.lastFall.Store(now.Unix())

	reply := new(dns.Msg)
	reply.SetReply(req)
	hdr := dns.RR_Header{Name: q.Name, Class: dns.ClassINET, Ttl: 30}
	if wantV6 {
		hdr.Rrtype = dns.TypeAAAA
		reply.Answer = append(reply.Answer, &dns.AAAA{Hdr: hdr, AAAA: ans.IP.AsSlice()})
	} else {
		hdr.Rrtype = dns.TypeA
		reply.Answer = append(reply.Answer, &dns.A{Hdr: hdr, A: ans.IP.AsSlice()})
	}
	_ = w.WriteMsg(reply)
}

// tryUpstreams forwards the query to each upstream in order and returns
// the first response, or nil when none answered inside the budget.
func (f *Forwarder) tryUpstreams(req *dns.Msg) *dns.Msg {
	client := &dns.Client{Net: "udp", Timeout: f.timeout}
	for _, up := range f.upstreams() {
		up = strings.TrimSpace(up)
		if up == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(up); err != nil {
			up = net.JoinHostPort(up, "53")
		}
		resp, _, err := client.Exchange(req, up)
		if err == nil && resp != nil {
			return resp
		}
	}
	return nil
}

// recordLiveAnswers feeds the observed tier from a live response.
func (f *Forwarder) recordLiveAnswers(resp *dns.Msg) {
	now := time.Now()
	for _, rr := range resp.Answer {
		switch a := rr.(type) {
		case *dns.A:
			if ip, ok := netip.AddrFromSlice(a.A.To4()); ok {
				_ = f.store.RecordObservation(a.Hdr.Name, ip.String(), now)
			}
		case *dns.AAAA:
			if ip, ok := netip.AddrFromSlice(a.AAAA); ok {
				_ = f.store.RecordObservation(a.Hdr.Name, ip.String(), now)
			}
		}
	}
}
