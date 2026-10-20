package mobile

import (
	"encoding/json"
	"github.com/Septonious/multidns/core"
	"net"
	"testing"
	"time"
)

func TestStopCancelsPendingStartup(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "probe", true: "discovery"}[automatic], func(t *testing.T) {
			packet, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer packet.Close()
			received := make(chan struct{}, 1)
			go func() {
				b := make([]byte, 4096)
				for {
					if _, _, err := packet.ReadFrom(b); err != nil {
						return
					}
					select {
					case received <- struct{}{}:
					default:
					}
				}
			}()
			profile, _ := json.Marshal(core.Profile{Domain: "t.test", Key: "0123456789abcdef", Groups: []core.Group{{Resolvers: []string{packet.LocalAddr().String()}}}, Automatic: automatic, TimeoutMS: 30000})
			engine := NewEngine()
			done := make(chan error, 1)
			go func() { done <- engine.Start(string(profile), 0, nil) }()
			select {
			case <-received:
			case <-time.After(2 * time.Second):
				t.Fatal("startup didn't send probe")
			}
			responsive := make(chan struct{})
			go func() { engine.Stats(); engine.Stop(); close(responsive) }()
			select {
			case <-responsive:
			case <-time.After(250 * time.Millisecond):
				t.Fatal("Stats/Stop blocked behind startup")
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("canceled startup succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("startup ignored cancellation")
			}
			if engine.Stats() != "{}" {
				t.Fatal("canceled engine published a client")
			}
			if err := engine.Start(string(profile), 0, nil); err == nil {
				t.Fatal("late startup resurrected a stopped engine")
			}
		})
	}
}

func TestCancelParameterTestBeforeWorkerStarts(t *testing.T) {
	engine := NewEngine()
	engine.CancelParameterTest()
	profile, _ := json.Marshal(core.Profile{Domain: "t.test", Key: "0123456789abcdef", Groups: []core.Group{{Resolvers: []string{"127.0.0.1:53"}}}})
	report, err := engine.FindOptimalParameters(string(profile), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var result core.TuningReport
	if err := json.Unmarshal([]byte(report), &result); err != nil || !result.Cancelled || len(result.Settings) > 0 {
		t.Fatalf("early cancel ignored: %v %s", err, report)
	}
}

type latencyEvents struct{}

func (latencyEvents) OnEvent(string) {}

func TestStopCancelsLatencyTest(t *testing.T) {
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	seen := make(chan struct{})
	go func() { b := make([]byte, 4096); packet.ReadFrom(b); close(seen) }()
	profile, _ := json.Marshal(core.Profile{Domain: "t.test", Key: "0123456789abcdef", Groups: []core.Group{{Resolvers: []string{packet.LocalAddr().String()}}}, TimeoutMS: 30000})
	engine := NewEngine()
	done := make(chan error, 1)
	go func() { done <- engine.TestLatency(string(profile), latencyEvents{}) }()
	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("test did not start")
	}
	engine.Stop()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled latency test reported success")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("latency test ignored cancellation")
	}
}
