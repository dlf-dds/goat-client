package daemon

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/dlf-dds/goat-client/internal/filedrop"
	"github.com/dlf-dds/goat-client/internal/innermesh"
	"github.com/dlf-dds/goat-client/internal/ipc"
)

// receivedRingCap bounds how many recent inbound-file records the daemon
// keeps for GetIncomingFiles.
const receivedRingCap = 64

// fileServer is the receive side of goatdrop the daemon's reconcile loop
// owns. *filedrop.Server satisfies it; tests inject a fake so the loop can
// be driven without binding a real socket.
type fileServer interface {
	ListenAndServe(ctx context.Context, addr string) error
}

// receivedRing is a small rolling record of completed inbound transfers,
// oldest evicted first. Safe for concurrent use.
type receivedRing struct {
	mu   sync.Mutex
	buf  []filedrop.Received
	head int
	n    int
}

func newReceivedRing(capacity int) *receivedRing {
	if capacity <= 0 {
		capacity = receivedRingCap
	}
	return &receivedRing{buf: make([]filedrop.Received, capacity)}
}

func (r *receivedRing) add(rec filedrop.Received) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.head] = rec
	r.head = (r.head + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
}

// list returns held records newest-first.
func (r *receivedRing) list() []filedrop.Received {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]filedrop.Received, 0, r.n)
	for i := 0; i < r.n; i++ {
		idx := (r.head - 1 - i + len(r.buf)) % len(r.buf)
		out = append(out, r.buf[idx])
	}
	return out
}

// authorizer returns a filedrop.Authorizer that admits a source overlay IP
// only when it matches a current inner-mesh peer, labeling it by FQDN. It
// reads the live peer list per call, so a revoked/departed peer stops being
// admitted without any subsystem restart. Fail-closed: no mesh, a status
// error, or an unknown source all deny.
func (d *Daemon) authorizer() filedrop.Authorizer {
	return filedrop.AuthorizerFunc(func(srcIP string) (string, bool) {
		d.mu.RLock()
		mesh := d.mesh
		d.mu.RUnlock()
		if mesh == nil {
			return "", false
		}
		peers, err := mesh.Peers()
		if err != nil {
			return "", false
		}
		for _, p := range peers {
			if p.IP == srcIP {
				label := p.FQDN
				if label == "" {
					label = p.IP
				}
				return label, true
			}
		}
		return "", false
	})
}

// runFileServer owns the goatdrop receive server's lifecycle for the
// daemon's lifetime: it runs the server bound to the local tunnel IP while
// the mode includes the inner mesh and the mesh is up (and the local IP is
// known), and stops it otherwise. Blocks until ctx is cancelled; started as
// a goroutine by ServeIPC.
func (d *Daemon) runFileServer(ctx context.Context) {
	interval := d.cfg.PeerConnReconcileInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	var cancel context.CancelFunc
	running := false
	stop := func() {
		if running {
			cancel()
			running = false
		}
	}
	defer stop()

	reconcile := func() {
		d.mu.RLock()
		mesh := d.mesh
		m := d.currentMode
		d.mu.RUnlock()

		if mesh == nil || !m.IncludesNetbird() || mesh.State() != innermesh.StateUp {
			stop()
			return
		}
		if running {
			return
		}
		localIP, err := mesh.LocalIP()
		if err != nil || localIP == "" {
			// Need the tunnel IP to bind mesh-only; try again next tick.
			return
		}
		sctx, scancel := context.WithCancel(ctx)
		srv := d.fileServerFactory(d.inboxDir, d.authorizer(), d.received.add)
		addr := net.JoinHostPort(localIP, strconv.Itoa(filedrop.DefaultPort))
		go func() {
			if err := srv.ListenAndServe(sctx, addr); err != nil {
				d.logf("filedrop serve: %v", err)
			}
		}()
		cancel = scancel
		running = true
	}

	reconcile()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reconcile()
		}
	}
}

// GetIncomingFiles lists recent goatdrop transfers, newest first.
func (d *Daemon) GetIncomingFiles(_ context.Context) (ipc.GetIncomingFilesReply, error) {
	recs := d.received.list()
	reply := ipc.GetIncomingFilesReply{Files: make([]ipc.IncomingFile, 0, len(recs))}
	for _, r := range recs {
		reply.Files = append(reply.Files, ipc.IncomingFile{
			Name:   r.Name,
			Size:   r.Size,
			From:   r.From,
			FromIP: r.FromIP,
			Path:   r.Path,
			At:     r.At,
		})
	}
	return reply, nil
}
