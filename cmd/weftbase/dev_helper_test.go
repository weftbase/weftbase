package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// The helper process `weftbase dev`'s tests run as the app: this test
// binary re-executed with WEFT_DEV_HELPER set (TestMain hands it here
// before any test runs). Modes:
//
//	app        prints its pid and the variables weftbase dev sets, then
//	           — per WEFT_DEV_HELPER_EXIT — exits with that code, or
//	           sleeps until a signal; WEFT_DEV_HELPER_GRANDCHILD=<dir>
//	           first starts a grandchild that writes <dir>/<app pid>.
//	grandchild writes its pid to WEFT_DEV_HELPER_PIDFILE and sleeps
//	           until a signal (its group's).
func devHelper(mode string) int {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	switch mode {
	case "grandchild":
		if err := os.WriteFile(os.Getenv("WEFT_DEV_HELPER_PIDFILE"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			return 9
		}
		<-sigs
		return 0
	case "app":
	default:
		return 8
	}
	fmt.Printf("helper pid=%d WEFT_ENV=%s WEFTBASE_URL=%s WEFTBASE_TOKEN=%s OTEL_EXPORTER_OTLP_ENDPOINT=%s OTEL_EXPORTER_OTLP_HEADERS=%s\n", os.Getpid(),
		os.Getenv("WEFT_ENV"), os.Getenv("WEFTBASE_URL"), os.Getenv("WEFTBASE_TOKEN"),
		os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	if code := os.Getenv("WEFT_DEV_HELPER_EXIT"); code != "" {
		n, _ := strconv.Atoi(code)
		return n
	}
	if dir := os.Getenv("WEFT_DEV_HELPER_GRANDCHILD"); dir != "" {
		gc := exec.Command(os.Args[0])
		gc.Env = append(os.Environ(), "WEFT_DEV_HELPER=grandchild",
			"WEFT_DEV_HELPER_PIDFILE="+filepath.Join(dir, strconv.Itoa(os.Getpid())))
		if err := gc.Start(); err != nil {
			return 7
		}
		go func() { _ = gc.Wait() }()
	}
	sig := <-sigs
	fmt.Printf("helper pid=%d got %v\n", os.Getpid(), sig)
	// Give a grandchild the same moment to go (it got the signal too).
	time.Sleep(10 * time.Millisecond)
	return 0
}
