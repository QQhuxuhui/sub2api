package main

import (
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"
)

// startPprofServer serves the Go profiler on DEBUG_PPROF_ADDR. It is off when
// the variable is empty and refuses anything but a loopback address, because
// these endpoints expose memory and request details.
func startPprofServer() {
	addr := strings.TrimSpace(os.Getenv("DEBUG_PPROF_ADDR"))
	if addr == "" {
		return
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		log.Printf("pprof disabled: invalid DEBUG_PPROF_ADDR %q: %v", addr, err)
		return
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		log.Printf("pprof disabled: DEBUG_PPROF_ADDR %q is not a loopback address", addr)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("pprof listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("pprof server stopped: %v", err)
		}
	}()
}
