package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/runtimeskills"
)

// AP_BENCH_AGENT_VERSION optionally selects a real version to copy into a
// disposable fixture. Never construct a registry on the deployment's roots.
func BenchmarkRuntimeAdmission(b *testing.B) {
	for _, extraMiB := range []int{0, 64} {
		for _, concurrency := range []int{1, 8} {
			b.Run(fmt.Sprintf("extra%dMiB/concurrent%d", extraMiB, concurrency), func(b *testing.B) {
				root := b.TempDir()
				agentRoot := filepath.Join(root, "ru-agents")
				dir := filepath.Join(agentRoot, "bench", "revision")
				b.Cleanup(func() { _ = runtimeskills.Remove(root) })
				if err := os.MkdirAll(filepath.Join(dir, "skills"), 0700); err != nil {
					b.Fatal(err)
				}
				if source := os.Getenv("AP_BENCH_AGENT_VERSION"); source != "" {
					if err := copyRuntimePath(source, dir); err != nil {
						b.Fatal(err)
					}
					dirs, err := runtimeskills.Dirs(source)
					if err != nil {
						b.Fatal(err)
					}
					for _, src := range dirs {
						dst := filepath.Join(root, "ru-skills", filepath.Base(src))
						if err := copyRuntimePath(src, dst); err != nil {
							b.Fatal(err)
						}
					}
				} else if err := runtimeskills.WriteReferences(dir, nil); err != nil {
					b.Fatal(err)
				}
				if extraMiB > 0 {
					f, err := os.Create(filepath.Join(dir, "large-resource.bin"))
					if err != nil {
						b.Fatal(err)
					}
					buf := make([]byte, 1<<20)
					for i := 0; i < extraMiB; i++ {
						if _, err := f.Write(buf); err != nil {
							b.Fatal(err)
						}
					}
					if err := f.Close(); err != nil {
						b.Fatal(err)
					}
				}
				expected, err := runtimeskills.DigestExcept(dir, ".revision", ".content-digest")
				if err != nil {
					b.Fatal(err)
				}
				refs, err := runtimeskills.References(dir)
				if err != nil {
					b.Fatal(err)
				}
				if err := runtimeskills.Seal(dir); err != nil {
					b.Fatal(err)
				}
				r := &FileRegistry{agents: map[string]AgentDefinition{"bench": {Key: "bench", RuntimeDir: dir, RuntimeRevision: "revision"}}, assembler: &runtimeAgentAssembler{root: agentRoot, contentDigests: map[string]string{dir: expected}, versionSkills: map[string][]runtimeskills.Reference{dir: refs}}}
				var samples []time.Duration
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					var wg sync.WaitGroup
					durations := make([]time.Duration, concurrency)
					start := make(chan struct{})
					for j := 0; j < concurrency; j++ {
						wg.Add(1)
						go func(j int) {
							defer wg.Done()
							<-start
							t := time.Now()
							_, release, ok := r.AcquireAgentRuntime("bench")
							durations[j] = time.Since(t)
							if !ok {
								b.Error("lease unavailable")
								return
							}
							release()
						}(j)
					}
					close(start)
					wg.Wait()
					samples = append(samples, durations...)
				}
				b.StopTimer()
				sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
				b.ReportMetric(float64(samples[len(samples)/2].Microseconds())/1000, "p50-ms")
				b.ReportMetric(float64(samples[(len(samples)-1)*95/100].Microseconds())/1000, "p95-ms")
			})
		}
	}
}
