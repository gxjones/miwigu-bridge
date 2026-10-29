package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "import" {
		if err := runImport(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := runServer(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runImport(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: helix-orchestrator import state.json")
	}
	store, err := openStoreFromEnv()
	if err != nil {
		return err
	}
	counts, err := store.ImportFile(args[0])
	if err != nil {
		return err
	}
	fmt.Printf("imported %d keys, %d requests (%d queued), %d access rows\n", counts.Keys, counts.Requests, counts.Queued, counts.Access)
	return nil
}

func runServer() error {
	adminPass := os.Getenv("ADMIN_PASSWORD")
	if adminPass == "" {
		return errors.New("ADMIN_PASSWORD is required")
	}
	adminUser := os.Getenv("ADMIN_USER")
	if adminUser == "" {
		adminUser = "admin"
	}
	store, err := openStoreFromEnv()
	if err != nil {
		return err
	}
	vms, err := openMicrovmAPI()
	if err != nil {
		return err
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	app := &server{
		adminUser: adminUser,
		adminPass: adminPass,
		passHash:  sha256.Sum256([]byte(adminPass)),
		store:     store,
		vms:       vms,
	}
	addr := net.JoinHostPort("0.0.0.0", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	cert, key := os.Getenv("TLS_CERT"), os.Getenv("TLS_KEY")
	scheme := "http"
	if cert != "" && key != "" {
		scheme = "https"
	}
	fmt.Printf("listening on %s (%s)\n", port, scheme)
	srv := &http.Server{
		Handler:           lambdaHTTPS(app.routes()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	if scheme == "https" {
		err = srv.ServeTLS(ln, cert, key)
	} else {
		err = srv.Serve(ln)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// lambdaHTTPS marks Function URL requests as HTTPS when the Web Adapter did
// not already set X-Forwarded-Proto. The session cookie uses that header.
func lambdaHTTPS(next http.Handler) http.Handler {
	if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-Proto") == "" {
			r.Header.Set("X-Forwarded-Proto", "https")
		}
		next.ServeHTTP(w, r)
	})
}
