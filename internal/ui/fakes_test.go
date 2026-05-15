package ui

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dlf-dds/goat-client/internal/ipc"
)

// fakeClient is a deterministic, test-controlled stand-in for ipc.Client.
// Tests seed canned responses + record method calls; no goroutines or
// timers run inside, so assertions can fire immediately after the call
// completes and there are no flaky waits.
type fakeClient struct {
	mu sync.Mutex

	status      *ipc.StatusInfo
	statusErr   error
	bundleReply *ipc.BundleInfo
	bundleErr   error
	diags       *ipc.Diagnostics
	diagsErr    error
	curMode     string
	modeErr     error
	setModePrev string
	setModeErr  error

	// connectErr / disconnectErr drive the next call's failure mode.
	connectErr    error
	disconnectErr error

	// Recorded calls.
	imported      [][]byte
	connectCalls  int
	disconnCalls  int
	setModeCalls  []string
	getModeCalls  int
	getDiagsCalls int
	getStatCalls  int
	closeCalls    int
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		status: &ipc.StatusInfo{State: ipc.StateDisconnected, Mode: "combined"},
		diags:  &ipc.Diagnostics{LogTail: []string{}},
	}
}

func (c *fakeClient) ImportBundle(_ context.Context, raw []byte) (*ipc.BundleInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.imported = append(c.imported, append([]byte(nil), raw...))
	if c.bundleErr != nil {
		return nil, c.bundleErr
	}
	return c.bundleReply, nil
}

func (c *fakeClient) GetStatus(_ context.Context) (*ipc.StatusInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getStatCalls++
	if c.statusErr != nil {
		return nil, c.statusErr
	}
	if c.status == nil {
		return &ipc.StatusInfo{}, nil
	}
	snapshot := *c.status
	return &snapshot, nil
}

func (c *fakeClient) Connect(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connectCalls++
	return c.connectErr
}

func (c *fakeClient) Disconnect(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnCalls++
	return c.disconnectErr
}

func (c *fakeClient) GetDiagnostics(_ context.Context) (*ipc.Diagnostics, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getDiagsCalls++
	if c.diagsErr != nil {
		return nil, c.diagsErr
	}
	if c.diags == nil {
		return &ipc.Diagnostics{}, nil
	}
	snapshot := *c.diags
	snapshot.LogTail = append([]string(nil), c.diags.LogTail...)
	return &snapshot, nil
}

func (c *fakeClient) GetMode(_ context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getModeCalls++
	if c.modeErr != nil {
		return "", c.modeErr
	}
	return c.curMode, nil
}

func (c *fakeClient) SetMode(_ context.Context, m string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setModeCalls = append(c.setModeCalls, m)
	if c.setModeErr != nil {
		return "", c.setModeErr
	}
	prev := c.setModePrev
	c.setModePrev = m
	c.curMode = m
	return prev, nil
}

func (c *fakeClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeCalls++
	return nil
}

// snapshotImported returns a copy of every ImportBundle payload the
// client has been handed, so assertions don't race the mutex.
func (c *fakeClient) snapshotImported() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.imported))
	for i, b := range c.imported {
		out[i] = append([]byte(nil), b...)
	}
	return out
}

// errBoom is the canned error tests pass through statusErr/bundleErr/etc.
// to exercise failure paths. Keeping it package-level keeps assertions
// concise (errors.Is(err, errBoom)) without each test inventing its own.
var errBoom = errors.New("boom")

// stableTime returns a fixed timestamp so handshake-formatting tests
// don't drift with time.Now().
func stableTime() time.Time {
	return time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
}
