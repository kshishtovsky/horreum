package metrics

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestMetricsRegistryAndExposition(t *testing.T) {
	cHash := RegisterCounter("test_counter", "Test counter help", []Label{{Name: "env", Value: "test"}})
	// Re-register same counter should return existing hash
	if cHash2 := RegisterCounter("test_counter", "Test counter help", []Label{{Name: "env", Value: "test"}}); cHash2 != cHash {
		t.Errorf("expected same hash for re-register")
	}

	IncCounter(cHash)
	IncCounterBy(cHash, 5)

	gHash := RegisterGauge("test_gauge", "Test gauge help", []Label{{Name: "type", Value: "memory"}})
	if gHash2 := RegisterGauge("test_gauge", "Test gauge help", []Label{{Name: "type", Value: "memory"}}); gHash2 != gHash {
		t.Errorf("expected same hash for re-register")
	}
	SetGauge(gHash, 100)
	AddGauge(gHash, 50)

	hHash := RegisterHistogram("test_hist", "Test hist help", []Label{{Name: "cmd", Value: "get"}}, []float64{0.01, 0.1, 1.0})
	if hHash2 := RegisterHistogram("test_hist", "Test hist help", []Label{{Name: "cmd", Value: "get"}}, []float64{0.01, 0.1, 1.0}); hHash2 != hHash {
		t.Errorf("expected same hash for re-register")
	}
	Observe(hHash, 0.005)
	Observe(hHash, 0.05)
	Observe(hHash, 2.0)

	var buf bytes.Buffer
	if err := WriteText(&buf); err != nil {
		t.Fatalf("WriteText failed: %v", err)
	}

	out := buf.String()
	if !bytes.Contains([]byte(out), []byte("test_counter")) || !bytes.Contains([]byte(out), []byte("test_gauge")) {
		t.Errorf("expected text output to contain registered metrics, got:\n%s", out)
	}
}

func TestDefaultAndNoopRecorder(t *testing.T) {
	nr := NoopRecorder{}
	nr.ObserveSet("ok", time.Millisecond)
	nr.ObserveGet("ok", time.Millisecond)
	nr.ObserveDel("err", time.Millisecond)

	rec := NewRecorder()
	rec.ObserveSet("ok", time.Millisecond)
	rec.ObserveSet("err", 5*time.Millisecond)
	rec.ObserveGet("ok", 2*time.Millisecond)
	rec.ObserveGet("err", 10*time.Millisecond)
	rec.ObserveDel("ok", time.Microsecond)
	rec.ObserveDel("err", 20*time.Millisecond)
}

func TestMetricsServer(t *testing.T) {
	srv, err := NewMetricsServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewMetricsServer failed: %v", err)
	}

	srv.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	addr := srv.Addr().String()

	// Test /healthz
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ok" {
		t.Errorf("expected 'ok', got %q", string(body))
	}

	// Test /metrics
	resp, err = http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics failed: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}
}
