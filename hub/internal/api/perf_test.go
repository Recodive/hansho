package api

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// TestPerfSendDeliver measures one message from POST /api/v1/messages to its push (worker pass included).
// Run: HUB_PERF=1 go test ./internal/api -run TestPerfSendDeliver -v
func TestPerfSendDeliver(t *testing.T) {
	if os.Getenv("HUB_PERF") == "" {
		t.Skip("set HUB_PERF=1")
	}
	e := newEnv(t)
	e.channel("ops", "active", "critical")
	for _, n := range []string{"phone", "watch"} {
		d := e.newDevice(n)
		d.pair("alice")
	}
	m := msg("alice", "ops")
	m["data"] = map[string]any{"host": "db-1"}
	send := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if code, _, raw := e.send(m); code != 202 {
				b.Fatalf("%d %s", code, raw)
			}
		}
	})
	t.Logf("send:   %s %s", send, send.MemString())
	// The worker now has send.N queued messages; time draining them in passes of the real batch size.
	n := send.N
	work := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			e.runWorker()
		}
	})
	t.Logf("worker: %s %s (drained %d messages, %d pushes)", work, work.MemString(), n, len(e.relay.pushes()))
	idle := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			e.runWorker()
		}
	})
	t.Logf("idle worker pass: %s %s", idle, idle.MemString())
	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	rss := ""
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "VmRSS") || strings.HasPrefix(l, "VmHWM") {
				rss += strings.Join(strings.Fields(l), " ") + " "
			}
		}
	}
	t.Logf("mem: go heap inuse %d KB, go sys %d KB, %s", ms.HeapInuse>>10, ms.Sys>>10, rss)
}
