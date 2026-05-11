package log

import (
	"bytes"
	"sync"
	"testing"
)

// TestConcurrentLogWritesRace tests that concurrent calls to log() do not
// cause data races when writing to a shared output buffer.
// Run with: go test -race -run TestConcurrentLogWritesRace ./pkg/log/
func TestConcurrentLogWritesRace(t *testing.T) {
	var buf bytes.Buffer // contended buffer
	logger := New(LevelDebug, &buf)

	const goroutines = 8
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				logger.Info("goroutine %d iteration %d", id, j)
			}
		}(i)
	}
	wg.Wait()
}
