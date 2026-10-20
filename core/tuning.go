package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const opTune byte = 9

// Deterministic authenticated, incompressible test payload, with no upstream
// connection or persistent server state. Nonce makes every test independent.
func tuningData(f frame) []byte {
	size := min(chunkSize, max(16, int(f.Seq)))
	out := make([]byte, size+1)
	seed := make([]byte, 16+len(f.Data))
	binary.BigEndian.PutUint64(seed, f.Nonce)
	copy(seed[16:], f.Data)
	for i := 0; i < size; i += 32 {
		binary.BigEndian.PutUint64(seed[8:], uint64(i))
		block := sha256.Sum256(seed)
		copy(out[i+1:], block[:])
	}
	return out
}

type TuningTrial struct {
	Direction      string  `json:"direction"`
	MTU            int     `json:"mtu"`
	Workers        int     `json:"workers"`
	UploadMTU      int     `json:"upload_mtu,omitempty"`
	Sent           int     `json:"sent"`
	Received       int     `json:"received"`
	P95MS          float64 `json:"p95_ms"`
	BytesPerSecond float64 `json:"bytes_per_second"`
}
type ResolverTuning struct {
	Endpoint   string           `json:"endpoint"`
	Settings   ResolverSettings `json:"settings"`
	BaselineMS float64          `json:"baseline_ms"`
	Trials     []TuningTrial    `json:"trials"`
	Error      string           `json:"error,omitempty"`
}
type TuningReport struct {
	Started   time.Time                   `json:"started"`
	Finished  time.Time                   `json:"finished"`
	Cancelled bool                        `json:"cancelled"`
	Resolvers []ResolverTuning            `json:"resolvers"`
	Settings  map[string]ResolverSettings `json:"resolver_settings"`
}

func (r TuningReport) JSON() string { b, _ := json.Marshal(r); return string(b) }

func (c *Client) tuneTrial(ctx context.Context, addr, direction string, mtu, workers, count int, payload ...int) TuningTrial {
	trial := TuningTrial{Direction: direction, MTU: mtu, Workers: workers, Sent: count}
	var mu sync.Mutex
	times := []float64{}
	total := 0
	uploadSize := c.uploadSize
	if len(payload) > 0 {
		uploadSize = payload[0]
	}
	if direction == "tx" {
		trial.UploadMTU = uploadSize
	}
	start := time.Now()
	var wg sync.WaitGroup
	jobs := make(chan int)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				f := frame{Op: opTune, Session: c.session, Nonce: freshNonce(), Seq: uint32(downloadCapacityMTU(c.Profile, mtu))}
				size := int(f.Seq)
				if direction == "tx" {
					f.Data = make([]byte, uploadSize)
					for i := range f.Data {
						f.Data[i] = byte(f.Nonce>>uint(i%8*8)) ^ byte(i)
					}
					f.Seq = 16
					size = len(f.Data)
				}
				m, err := query(f, c.Profile)
				if err != nil {
					continue
				}
				m.SetEdns0(uint16(mtu), false)
				probeCtx, cancel := context.WithTimeout(ctx, time.Duration(min(1200, c.Profile.TimeoutMS))*time.Millisecond)
				at := time.Now()
				reply, err := c.exchangeDNS(probeCtx, m, addr, "udp")
				cancel()
				if err != nil || reply.Truncated || reply.Rcode != dns.RcodeSuccess {
					continue
				}
				answer, err := parseResponse(reply, c.Profile.Key)
				if err != nil || answer.Op != f.Op || answer.Nonce != f.Nonce || answer.Session != f.Session || !bytes.Equal(answer.Data, tuningData(f)) {
					continue
				}
				mu.Lock()
				times = append(times, float64(time.Since(at).Microseconds())/1000)
				total += size
				mu.Unlock()
			}
		}()
	}
loop:
	for i := 0; i < count; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			trial.Sent = i
			break loop
		}
	}
	close(jobs)
	wg.Wait()
	trial.Received = len(times)
	trial.BytesPerSecond = float64(total) / max(.001, time.Since(start).Seconds())
	sort.Float64s(times)
	if len(times) > 0 {
		trial.P95MS = times[min(len(times)-1, int(float64(len(times))*.95))]
	}
	return trial
}

// FindOptimalParameters measures this server's actual authenticated TXT path.
// Results are recommendations for this link at this time, never silent edits.
func (c *Client) FindOptimalParameters(ctx context.Context, emit func(string)) TuningReport {
	report := TuningReport{Started: time.Now(), Settings: map[string]ResolverSettings{}}
	for _, addr := range c.Profile.ResolverPool() {
		if ctx.Err() != nil {
			break
		}
		result := ResolverTuning{Endpoint: addr}
		event := func(stage string) {
			if emit != nil {
				b, _ := json.Marshal(struct {
					Endpoint, Stage string
					Result          ResolverTuning `json:"result"`
				}{addr, stage, result})
				emit(string(b))
			}
		}
		event("Testing authenticated payload sizes")
		for _, mtu := range []int{512, 768, 900, 1024, 1232} {
			trial := c.tuneTrial(ctx, addr, "rx", mtu, 1, 3)
			result.Trials = append(result.Trials, trial)
			if trial.Received == trial.Sent && trial.Sent == 3 {
				result.Settings.MTU = mtu
				if result.BaselineMS == 0 || trial.P95MS < result.BaselineMS {
					result.BaselineMS = trial.P95MS
				}
			}
			if ctx.Err() != nil {
				break
			}
		}
		if result.Settings.MTU == 0 {
			result.Error = "No full-size authenticated replies. Check server version, key and resolver reachability."
		} else {
			// Long query names can fail even when a small authenticated probe works.
			for _, size := range []int{c.uploadSize, min(c.uploadSize, 96), min(c.uploadSize, 64), min(c.uploadSize, 32), 16} {
				trial := c.tuneTrial(ctx, addr, "tx", result.Settings.MTU, 1, 3, size)
				result.Trials = append(result.Trials, trial)
				if trial.Sent == 3 && trial.Received == 3 {
					result.Settings.UploadMTU = size
					break
				}
				if ctx.Err() != nil {
					break
				}
			}
			if result.Settings.UploadMTU == 0 {
				result.Error = "No reliable upload payload size found"
				report.Resolvers = append(report.Resolvers, result)
				event("Finished")
				continue
			}
			// TX is tested separately because the DNS question has a much smaller
			// payload budget than a TXT response, and their rate limits can differ.
			for _, direction := range []string{"tx", "rx"} {
				event("Testing " + direction + " concurrency and loaded RTT")
				bestRate := 0.0
				selected := 1
				for _, workers := range []int{1, 2, 4, 8, 12, 16, 24, 32} {
					trial := c.tuneTrial(ctx, addr, direction, result.Settings.MTU, workers, max(6, workers*2), result.Settings.UploadMTU)
					result.Trials = append(result.Trials, trial)
					// Allow normal jitter, but reject a concurrency step that adds queue
					// delay or loss. A 10% rate gain is needed to justify extra workers.
					stable := trial.Sent > 0 && trial.Received == trial.Sent && trial.P95MS <= result.BaselineMS+max(float64(c.Profile.LatencyTargetMS), result.BaselineMS*.5)
					if stable && trial.BytesPerSecond > bestRate*1.10 {
						bestRate = trial.BytesPerSecond
						selected = workers
					}
					if !stable || ctx.Err() != nil {
						break
					}
				}
				if direction == "tx" {
					result.Settings.TXWorkers = selected
				} else {
					result.Settings.RXWorkers = selected
				}
			}
			if ctx.Err() == nil {
				report.Settings[addr] = result.Settings
			}
		}
		report.Resolvers = append(report.Resolvers, result)
		event("Finished")
	}
	report.Finished = time.Now()
	report.Cancelled = ctx.Err() != nil
	return report
}

var ErrTuningBusy = errors.New("parameter test is already running")

func (r TuningReport) ValidateApplicable() error {
	if r.Cancelled {
		return context.Canceled
	}
	if len(r.Settings) == 0 {
		return fmt.Errorf("no working resolver parameters were found")
	}
	return nil
}
