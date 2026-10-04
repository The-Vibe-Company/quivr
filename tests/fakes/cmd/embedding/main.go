package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/The-Vibe-Company/quivr/tests/fakes/embedding"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "loopback address")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("listen must be a loopback IP address")
	}
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Handler: embedding.New(), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := server.Serve(l); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	if err := json.NewEncoder(os.Stdout).Encode(map[string]string{"url": "http://" + l.Addr().String()}); err != nil {
		log.Fatal(err)
	}
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
}
