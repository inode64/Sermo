package metrics

import (
	"testing"
	"time"
)

type observationReader struct {
	fakeReader
	calls map[string]int
}

func (r *observationReader) ProcessCPU(pid int) (uint64, bool) {
	r.calls["CPU"]++
	return r.fakeReader.ProcessCPU(pid)
}

func (r *observationReader) ProcessRSS(pid int) (uint64, bool) {
	r.calls["RSS"]++
	return r.fakeReader.ProcessRSS(pid)
}

func (r *observationReader) ProcessFDs(pid int) (uint64, bool) {
	r.calls["FDs"]++
	return r.fakeReader.ProcessFDs(pid)
}

func (r *observationReader) ProcessThreads(pid int) (uint64, bool) {
	r.calls["Threads"]++
	return r.fakeReader.ProcessThreads(pid)
}

func (r *observationReader) ProcessIO(pid int) (uint64, uint64, bool) {
	r.calls["IO"]++
	return r.fakeReader.ProcessIO(pid)
}

func TestProcessObservationSharesReadsAndKeepsIndependentRates(t *testing.T) {
	for _, available := range []bool{true, false} {
		name := "available"
		if !available {
			name = "unavailable"
		}
		t.Run(name, func(t *testing.T) {
			threadReads := []int{}
			r := &observationReader{
				cpu: map[int]uint64{}, rss: map[int]uint64{}, threads: map[int]uint64{},
				hz: 100, ncpu: 2, memTotal: 4096,
				threadCPU: map[int]map[int]uint64{7: {8: 0}}, threadCPUReads: &threadReads,
				calls: map[string]int{}}
			checks, live := New(r), New(r)
			at := time.Unix(1000, 0)
			checks.Now = func() time.Time { return at }
			live.Now = checks.Now
			for cycle := 1; cycle <= 3; cycle++ {
				if available {
					r.cpu[7] = uint64(cycle * 100)
					r.rss[7] = 1024
					r.threads[7] = 2
					r.threadCPU[7][8] = uint64(cycle * 100)
				}
				observation := NewProcessObservation(r, []int{7})
				cpu := live.SampleServiceCPU("app", observation)
				snap := checks.SampleServiceObserved("app", observation)
				// Runtime history consumes the same counters after both samplers.
				observation.ProcessRSS(7)
				observation.ProcessIO(7)
				observation.ProcessFDs(7)
				observation.ProcessThreads(7)
				for _, metric := range []string{"CPU", "RSS", "IO", "FDs", "Threads"} {
					if r.calls[metric] != cycle {
						t.Fatalf("%s reads = %d, want %d", metric, r.calls[metric], cycle)
					}
				}
				if cycle > 1 && available && (cpu.CPU.Percent != 50 || snap[MetricCPU].Percent != 50) {
					t.Fatalf("independent rates: live=%+v check=%+v", cpu.CPU, snap[MetricCPU])
				}
				if snap[MetricMemory].Ready != available {
					t.Fatalf("memory readiness = %+v", snap[MetricMemory])
				}
				at = at.Add(time.Second)
			}
			wantThreadReads := 0
			if available {
				wantThreadReads = 2
			}
			if len(threadReads) != wantThreadReads {
				t.Fatalf("thread reads = %v, want %d", threadReads, wantThreadReads)
			}
			// Operations deliberately bypass the observation and take a fresh read.
			checks.SampleService("operation", []int{7})
			if r.calls["CPU"] != 4 {
				t.Fatal("operation reused monitoring observation")
			}
		})
	}
}
