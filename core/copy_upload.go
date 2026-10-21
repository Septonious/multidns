package core

import (
	"context"
	"io"
	"net"
	"time"
)

// Keep the negotiated selective-ACK window moving across local TCP read
// boundaries. io.Copy's synchronous Write calls otherwise drain every ~32KiB
// batch before reading the next one, even when many resolvers have spare room.
// Source buffering is bounded to three 32KiB blocks (current, queued, reading);
// every actual DNS request still passes global/per-flow/per-path admission.
func (s *Conn) copyUpload(src net.Conn) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx, cancel := s.opContext(false)
	defer cancel()
	blocks := make(chan readResult, 1)
	readerDone := make(chan struct{})
	stopReader := context.AfterFunc(ctx, func() { _ = src.SetReadDeadline(time.Now()) })
	defer func() {
		cancel()
		if stopReader() {
			_ = src.SetReadDeadline(time.Now())
		}
		<-readerDone
	}()
	go func() {
		defer close(readerDone)
		for {
			b := make([]byte, 32*1024)
			n, err := src.Read(b)
			select {
			case blocks <- readResult{b[:n], err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	type completion struct {
		index, size int
		err         error
	}
	results := make(chan completion, largeUploadWindow)
	pending := make(map[int]completion)
	var block []byte
	var sourceErr error
	var total int64
	next, acknowledged, completed := 0, 0, 0
	active := false
	setActive := func(on bool) {
		if active == on {
			return
		}
		active = on
		if on {
			s.client.activeUploads.Add(1)
		} else {
			s.client.activeUploads.Add(-1)
		}
	}
	defer func() { setActive(false); s.notifyActivity() }()
	for {
		if len(block) > 1024 {
			setActive(true)
		}
		window := s.window(opWrite)
		for len(block) > 0 && next-completed < window && next-acknowledged < max(64, s.writeLimit) {
			size := min(s.client.uploadSize, len(block))
			data := append([]byte(nil), block[:size]...)
			block = block[size:]
			index, seq := next, s.writeSeq
			next++
			s.writeSeq++
			bulk := active && s.traffic.Load() >= 4096
			go func() {
				_, err := s.client.rpcACK(ctx, frame{Op: opWrite, Stream: s.id, Seq: seq, Data: data, Bulk: bulk}, s.base, s.readSeq.Load)
				results <- completion{index, size, err}
			}()
		}
		if sourceErr != nil && len(block) == 0 && acknowledged == next {
			if sourceErr == io.EOF {
				return total, nil
			}
			return total, sourceErr
		}
		if completed == next && len(block) == 0 {
			setActive(false)
		}
		var input <-chan readResult
		if len(block) == 0 && sourceErr == nil {
			input = blocks
		}
		select {
		case result := <-results:
			completed++
			pending[result.index] = result
		case result := <-input:
			block, sourceErr = result.b, result.e
			if len(block) > 0 {
				s.notifyActivity()
			}
		case <-ctx.Done():
			return total, ctx.Err()
		}
		for {
			r, ok := pending[acknowledged]
			if !ok {
				break
			}
			if r.err != nil {
				return total, r.err
			}
			delete(pending, acknowledged)
			acknowledged++
			total += int64(r.size)
			s.client.uploaded.Add(uint64(r.size))
			s.traffic.Add(uint64(r.size))
		}
	}
}
