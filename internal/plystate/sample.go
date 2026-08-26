package plystate

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Sampler keeps a short in-memory history of per-instance CPU and memory —
// enough for a sparkline, deliberately nothing more (no metrics database;
// history dies with the process, Prometheus exists for the real thing).
type Sampler struct {
	paths    Paths
	mu       sync.Mutex
	series   map[string]*series // key: app.n
	interval time.Duration
}

type series struct {
	lastCPU  uint64 // cumulative usec (cgroup) or ticks (proc)
	lastSeen time.Time
	cpuPct   []float64 // ring, newest last
	memBytes []uint64
}

const ringLen = 40 // ~2 minutes at 3s

func NewSampler(p Paths, interval time.Duration) *Sampler {
	return &Sampler{paths: p, series: map[string]*series{}, interval: interval}
}

// Run samples forever; start it as a goroutine.
func (s *Sampler) Run() {
	for {
		if instances, err := List(s.paths); err == nil {
			s.sample(instances)
		}
		time.Sleep(s.interval)
	}
}

func (s *Sampler) sample(instances []Instance) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	now := time.Now()
	for _, inst := range instances {
		if !inst.Alive {
			continue
		}
		key := inst.Name()
		seen[key] = true
		cpu, mem, ok := s.read(inst)
		if !ok {
			continue
		}
		sr := s.series[key]
		if sr == nil {
			sr = &series{}
			s.series[key] = sr
		}
		if !sr.lastSeen.IsZero() && cpu >= sr.lastCPU {
			elapsed := now.Sub(sr.lastSeen).Microseconds()
			if elapsed > 0 {
				pct := float64(cpu-sr.lastCPU) / float64(elapsed) * 100
				sr.cpuPct = push(sr.cpuPct, pct)
			}
		}
		sr.lastCPU = cpu
		sr.lastSeen = now
		sr.memBytes = push(sr.memBytes, mem)
	}
	for key := range s.series {
		if !seen[key] {
			delete(s.series, key) // instance gone; its history goes too
		}
	}
}

// read returns cumulative CPU microseconds and current memory bytes.
// Rootful: the instance's cgroup (`ply-<app>.<n>`). Rootless: /proc fallback
// (pid 1 of the instance only — same trade `ply stats` makes).
func (s *Sampler) read(inst Instance) (cpu uint64, mem uint64, ok bool) {
	cg := filepath.Join(s.paths.Cgroup, fmt.Sprintf("ply-%s.%d", inst.App, inst.N))
	if st, err := os.Stat(cg); err == nil && st.IsDir() {
		cpu = readKeyed(filepath.Join(cg, "cpu.stat"), "usage_usec")
		mem = readUint(filepath.Join(cg, "memory.current"))
		return cpu, mem, true
	}
	// proc fallback: utime+stime ticks -> usec (USER_HZ=100), RSS pages -> bytes
	stat, err := os.ReadFile(filepath.Join(s.paths.Proc, fmt.Sprint(inst.PID), "stat"))
	if err != nil {
		return 0, 0, false
	}
	after := string(stat)
	if idx := strings.LastIndex(after, ")"); idx >= 0 {
		after = after[idx+2:]
	}
	fields := strings.Fields(after)
	if len(fields) < 22 {
		return 0, 0, false
	}
	utime, _ := strconv.ParseUint(fields[11], 10, 64) // field 14 overall
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	rssPages, _ := strconv.ParseUint(fields[21], 10, 64) // field 24 overall
	return (utime + stime) * 10_000, rssPages * uint64(os.Getpagesize()), true
}

func readKeyed(path, key string) uint64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, found := strings.CutPrefix(line, key+" "); found {
			n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}

func readUint(path string) uint64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return n
}

func push[T any](ring []T, v T) []T {
	ring = append(ring, v)
	if len(ring) > ringLen {
		ring = ring[len(ring)-ringLen:]
	}
	return ring
}

// Snapshot for one instance: sparklines and current values, render-ready.
type Stats struct {
	CPUSpark string
	CPUNow   string
	MemSpark string
	MemNow   string
}

func (s *Sampler) Stats(name string) Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	sr := s.series[name]
	if sr == nil {
		return Stats{CPUSpark: "", CPUNow: "-", MemSpark: "", MemNow: "-"}
	}
	st := Stats{
		CPUSpark: spark(sr.cpuPct, 0), // 0 = auto-scale
		MemSpark: sparkU(sr.memBytes),
	}
	if len(sr.cpuPct) > 0 {
		st.CPUNow = fmt.Sprintf("%.1f%%", sr.cpuPct[len(sr.cpuPct)-1])
	} else {
		st.CPUNow = "-"
	}
	if len(sr.memBytes) > 0 {
		st.MemNow = HumanBytes(sr.memBytes[len(sr.memBytes)-1])
	} else {
		st.MemNow = "-"
	}
	return st
}

// Unicode sparklines — terminal spirit, zero JavaScript.
var sparkRunes = []rune("▁▂▃▄▅▆▇█")

func spark(values []float64, max float64) string {
	if len(values) == 0 {
		return ""
	}
	if max <= 0 {
		for _, v := range values {
			if v > max {
				max = v
			}
		}
	}
	if max <= 0 {
		max = 1
	}
	var b strings.Builder
	for _, v := range values {
		idx := int(v / max * float64(len(sparkRunes)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkRunes) {
			idx = len(sparkRunes) - 1
		}
		b.WriteRune(sparkRunes[idx])
	}
	return b.String()
}

func sparkU(values []uint64) string {
	f := make([]float64, len(values))
	for i, v := range values {
		f[i] = float64(v)
	}
	return spark(f, 0)
}

func HumanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
