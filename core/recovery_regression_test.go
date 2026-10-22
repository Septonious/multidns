package core

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestRecoveryRejectsPreOutageRepliesAndGrowsFromSafeWindow(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 110, opWrite, nil, false)
	before := time.Now().Add(-time.Second)
	c.adaptiveMu.Lock()
	path := c.pathLocked(addr)
	path.limit, path.rx.limit = 64, 32
	c.markPathDownLocked(addr, time.Now())
	c.adaptiveMu.Unlock()
	c.recordStart(addr)
	c.recordEnd(addr, 100*time.Millisecond, 110, opWrite, nil, false, before)
	if c.healthSnapshot()[0].Healthy {
		t.Fatal("old in-flight success reopened circuit")
	}
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 0, opProbe, nil, false, time.Now())
	c.adaptiveMu.Lock()
	if !c.health[addr].Healthy || path.limit > 4 || path.rx.limit > 4 {
		t.Error("recovery restored overloaded window")
	}
	for range 100 {
		c.updateCapacityLocked(addr, 110, opWrite, nil, 40*time.Millisecond)
		c.updateCapacityLocked(addr, 768, opRead, nil, 40*time.Millisecond)
	}
	if path.limit > 4 || path.rx.limit > 4 {
		t.Error("old burst defeated recovery grace")
	}
	c.adaptiveMu.Unlock()
	c.recordStart(addr)
	c.recordEnd(addr, time.Second, 0, opWrite, context.DeadlineExceeded, false, before)
	h := c.healthSnapshot()[0]
	if !h.Healthy || h.HealthDowns != 1 || h.Recoveries != 1 || h.Failures != 0 || h.Timeouts != 1 {
		t.Fatalf("late expiration restarted outage: %+v", h)
	}
}

func TestRepeatedOutagesBackOffWithoutExtendingOnLateBatch(t *testing.T) {
	p, _ := testProfile(t)
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 0, opProbe, nil, false)
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	now := time.Now()
	for episode := 0; episode < 6; episode++ {
		c.markPathDownLocked(addr, now)
		path := c.pathLocked(addr)
		deadline := path.retryAt
		want := time.Duration(1<<uint(min(4, episode))) * 500 * time.Millisecond
		if deadline.Sub(now) != want {
			t.Fatalf("backoff=%s want %s", deadline.Sub(now), want)
		}
		c.markPathDownLocked(addr, now.Add(50*time.Millisecond))
		if path.retryAt != deadline {
			t.Fatal("late batch extended outage")
		}
		now = deadline.Add(time.Millisecond)
		c.recoverPathLocked(addr, now)
		now = now.Add(time.Millisecond)
	}
}

func TestBatchedReadChunksStayImmutableAndACKsAdvanceOnce(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	st := &serverStream{ready: ready, notify: make(chan struct{}), chunks: map[uint32][]byte{}, readSize: 32, readLimit: 128}
	payload := make([]byte, 1024)
	for i := range payload {
		payload[i] = byte(i * 17)
	}
	st.appendReadBatch(payload)
	var combined []byte
	for i := uint32(0); i < st.nextRead; i++ {
		combined = append(combined, st.chunks[i]...)
	}
	if len(st.chunks) != 32 || !bytes.Equal(combined, payload) {
		t.Fatal("batched data corrupted or split incorrectly")
	}
	s := &Server{streams: map[streamKey]*serverStream{{session: 1, stream: 1}: st}}
	for _, ack := range []uint32{5, 3, 5, 12} {
		got := s.handle(frame{Session: 1, Stream: 1, Op: opRead, Seq: 20, Ack: ack})
		if got[0] != statusOK || !bytes.Equal(got[1:], payload[20*32:21*32]) {
			t.Fatal("ACK changed immutable future chunk")
		}
	}
	if st.ackedRead != 12 || len(st.chunks) != 20 {
		t.Fatal("stale ACK regressed deletion watermark")
	}
}

func TestReadBudgetLearnsBeyondLegacyLimitAndBacksOffOnPoolDelay(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	now := time.Now()
	feed := func(n int, delay time.Duration) {
		for range n {
			now = now.Add(5 * time.Millisecond)
			c.leasedReads = c.readBudget() - 1
			c.updateReadBudgetLocked(768, delay, false, now.Add(-delay), now, true)
		}
	}
	feed(1000, 40*time.Millisecond)
	if c.readBudget() != 160 {
		t.Fatal("expanded against unnegotiated legacy server")
	}
	c.peerReadWindow.Store(largeReadWindow)
	feed(2000, 40*time.Millisecond)
	if c.readBudget() != 320 {
		t.Fatalf("saturated healthy pool still capped at %d", c.readBudget())
	}
	feed(1000, 300*time.Millisecond)
	if c.readBudget() >= 160 {
		t.Fatal("persistent loaded delay did not withdraw credits")
	}
	if c.readBudget() < 32 {
		t.Fatal("read recovery floor lost")
	}
	feed(3000, 40*time.Millisecond)
	if c.readBudget() != 320 {
		t.Fatal("RX budget did not recover without recreating the client")
	}
	c.leasedReads = 0
}

func TestReadWindowIsNegotiatedPerConnection(t *testing.T) {
	p, _ := testProfile(t)
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	conn, err := c.DialContext(context.Background(), "tcp", echoTCP(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.(*Conn).readLimit != largeReadWindow || c.peerReadWindow.Load() != largeReadWindow {
		t.Fatal("new read window not negotiated")
	}
}

func TestSparseReadLossDoesNotCompoundIntoGlobalCollapse(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	c.peerReadWindow.Store(128)
	now := time.Now()
	for i := 0; i < 1500; i++ {
		now = now.Add(5 * time.Millisecond)
		c.leasedReads = c.readBudget() - 1
		failed := i%67 == 0
		size := 768
		if failed {
			size = 0
		}
		c.updateReadBudgetLocked(size, 40*time.Millisecond, failed, now.Add(-40*time.Millisecond), now, true)
	}
	if c.readBudget() < 160 {
		t.Fatal("ordinary per-path ARQ loss collapsed the pool budget")
	}
	for i := 0; i < 1000; i++ {
		now = now.Add(5 * time.Millisecond)
		c.leasedReads = c.readBudget() - 1
		failed := i%3 == 0
		size := 768
		if failed {
			size = 0
		}
		c.updateReadBudgetLocked(size, 40*time.Millisecond, failed, now.Add(-40*time.Millisecond), now, true)
	}
	if c.readBudget() >= 160 {
		t.Fatal("severe aggregate loss did not reduce pool pressure")
	}
	c.leasedReads = 0
}

func TestACKCreditDoesNotWakeMissingDataWaiters(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	st := &serverStream{ready: ready, notify: make(chan struct{}), chunks: map[uint32][]byte{}, readSize: 32, readLimit: 128}
	st.appendReadBatch(make([]byte, 128*32))
	dataWake := st.notify
	creditWake := st.creditWake()
	s := &Server{streams: map[streamKey]*serverStream{{session: 1, stream: 1}: st}}
	s.handle(frame{Session: 1, Stream: 1, Op: opRead, Seq: 20, Ack: 1})
	select {
	case <-creditWake:
	default:
		t.Fatal("ACK did not release producer credit")
	}
	s.handle(frame{Session: 1, Stream: 1, Op: opRead, Seq: 20, Ack: 8})
	select {
	case <-creditWake:
	default:
		t.Fatal("batch ACK did not release producer")
	}
	select {
	case <-dataWake:
		t.Fatal("ACK woke readers without supplying data")
	default:
	}
	st.appendReadBatch(make([]byte, 8*32))
	select {
	case <-dataWake:
	default:
		t.Fatal("new immutable data did not wake readers")
	}
}

func TestReadNotificationsOnlyWakeRequestedSequenceAndReleaseOnClose(t *testing.T) {
	st := &serverStream{notify: make(chan struct{}), chunks: map[uint32][]byte{}, readSize: 32}
	one, two := st.waitForRead(0), st.waitForRead(1)
	st.appendReadBatch(make([]byte, 32))
	select {
	case <-one.wake:
	default:
		t.Fatal("ready chunk waiter not notified")
	}
	select {
	case <-two.wake:
		t.Fatal("unrelated sequence woke for another chunk")
	default:
	}
	st.releaseReadWait(0, one)
	st.closed = true
	st.signal()
	select {
	case <-two.wake:
	default:
		t.Fatal("close did not release missing sequence")
	}
	st.releaseReadWait(1, two)
	if len(st.readWait) != 0 {
		t.Fatal("read wait registrations leaked")
	}
}

func TestInteractiveBackupUsesReservedCreditButRespectsQueuedForeground(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	first, second := p.Groups[0].Resolvers[0], p.Groups[0].Resolvers[1]
	c.adaptiveMu.Lock()
	for _, addr := range []string{first, second} {
		c.health[addr] = &ResolverHealth{Healthy: true, Endpoint: addr, RTTMS: 40}
		c.pathLocked(addr).limit = 64
	}
	c.leasedData = cap(c.dataSlots) - 8
	c.pathLocked(second).flight = 63
	c.admissions = []*admission{{bulk: true, op: opWrite}}
	c.adaptiveMu.Unlock()
	if _, _, err := c.tryAlternative(first, opWrite, 1, true); err == nil {
		t.Fatal("bulk hedge consumed foreground reserve")
	}
	addr, release, err := c.tryAlternative(first, opWrite, 1, false)
	if err != nil || addr != second {
		t.Fatalf("interactive hedge denied reserved credit: %v", err)
	}
	release()
	c.adaptiveMu.Lock()
	c.admissions[0].bulk = false
	c.adaptiveMu.Unlock()
	if _, _, err := c.tryAlternative(first, opWrite, 1, false); err == nil {
		t.Fatal("hedge overtook queued foreground work")
	}
	c.adaptiveMu.Lock()
	c.admissions = nil
	c.leasedData = 0
	c.pathLocked(second).flight = 0
	c.adaptiveMu.Unlock()
}
