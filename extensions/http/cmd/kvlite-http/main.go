package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	kvlitehttp "github.com/webong/kvlite/extensions/http"
	"kvlite"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("kvlite-http", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	path := flags.String("path", "", "KVLite database directory to own")
	driver := flags.String("driver", "", "installed storage driver (defaults to Open default)")
	listen := flags.String("listen", "127.0.0.1:8089", "HTTP listen address")
	token := flags.String("token", "", "Bearer token required by clients")
	maxRequestBytes := flags.Int64("max-request-bytes", 64<<20, "maximum JSON request size")
	driverPaths := make(map[kvlite.DriverName]string)
	flags.Func("driver-path", "additional remote driver mapping in DRIVER=PATH form; repeatable", func(raw string) error {
		return addDriverPath(driverPaths, raw)
	})
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "kvlite-http: --path is required")
		return 2
	}
	if *driver == string(kvlite.DriverMemory) {
		fmt.Fprintln(os.Stderr, "kvlite-http: serving an ephemeral in-memory database; everything will be lost on exit")
	}

	dbOptions := make([]kvlite.Option, 0)
	if *driver != "" {
		dbOptions = append(dbOptions, kvlite.WithDriver(*driver))
	}
	db, err := kvlite.Open(*path, dbOptions...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kvlite-http: %v\n", err)
		return 1
	}
	defer db.Close()

	server, err := kvlitehttp.Serve(db, kvlitehttp.Options{
		ListenAddress:   *listen,
		BearerToken:     *token,
		MaxRequestBytes: *maxRequestBytes,
		DriverPaths:     driverPaths,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "kvlite-http: %v\n", err)
		return 1
	}
	defer server.Close()

	fmt.Printf("kvlite-http url=%s\n", server.URL())
	fmt.Println("kvlite-http: serving; press Ctrl-C to stop")

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	<-signals
	return 0
}

func addDriverPath(paths map[kvlite.DriverName]string, raw string) error {
	name, path, ok := strings.Cut(raw, "=")
	name = strings.TrimSpace(name)
	path = strings.TrimSpace(path)
	if !ok || name == "" || path == "" {
		return fmt.Errorf("driver mapping must use DRIVER=PATH")
	}
	driver := kvlite.DriverName(strings.ToLower(name))
	if _, exists := paths[driver]; exists {
		return fmt.Errorf("driver mapping for %q was supplied more than once", driver)
	}
	paths[driver] = path
	return nil
}
