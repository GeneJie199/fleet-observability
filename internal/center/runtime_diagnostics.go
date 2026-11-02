package center

import (
	"net/http"
	"runtime"
	"time"
)

var diagnosticsProcessStarted = time.Now()

// runtimeDiagnostics contains aggregate process counters only; no profiles or configuration.
type runtimeDiagnostics struct {
	Goroutines     int     `json:"goroutines"`
	HeapAllocBytes uint64  `json:"heapAllocBytes"`
	HeapSysBytes   uint64  `json:"heapSysBytes"`
	HeapInuseBytes uint64  `json:"heapInuseBytes"`
	SystemBytes    uint64  `json:"systemBytes"`
	GCCount        uint32  `json:"gcCount"`
	UptimeSeconds  float64 `json:"uptimeSeconds"`
}

func handleRuntimeDiagnostics(w http.ResponseWriter, _ *http.Request) {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	value := runtimeDiagnostics{
		Goroutines: runtime.NumGoroutine(), HeapAllocBytes: memory.HeapAlloc,
		HeapSysBytes: memory.HeapSys, HeapInuseBytes: memory.HeapInuse,
		SystemBytes: memory.Sys, GCCount: memory.NumGC,
		UptimeSeconds: time.Since(diagnosticsProcessStarted).Seconds(),
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, value)
}
