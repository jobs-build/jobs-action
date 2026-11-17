// jobs-action runs every TESTS.jobs under a project root on a jobs-iroh
// server and, in GitHub Actions, reports each test as a check run.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jobs-build/jobs-action/action"
)

// version is stamped by the release build.
var version = "dev"

type opts struct {
	server, addrs  string
	platform, root string
	dataDir        string
	list, version  bool
}

// newFlags declares the flags. The server and its addresses have no default
// here although the environment supplies one: a flag's default is printed by
// --help and by every flag error, and the server ID is a credential.
func newFlags(out io.Writer) (*flag.FlagSet, *opts) {
	o := &opts{}
	fs := flag.NewFlagSet("jobs-action", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		fmt.Fprintln(out, "usage: jobs-action [flags]")
		fmt.Fprintln(out, "\nRuns every TESTS.jobs under --root on a jobs-iroh server.")
		fmt.Fprintln(out)
		fs.PrintDefaults()
	}
	fs.StringVar(&o.server, "server", "", "jobs-iroh server endpoint ID; a credential, so prefer $JOBS_SERVER, which is used when the flag is not given")
	fs.StringVar(&o.addrs, "server-addr", "", "direct host:port addresses of the server, comma-separated (when not given: $JOBS_SERVER_ADDR)")
	fs.StringVar(&o.platform, "platform", "linux/amd64", "platform every test runs on")
	fs.StringVar(&o.root, "root", ".", "project root: the tree that is ingested and searched")
	fs.StringVar(&o.dataDir, "data-dir", "", "local store directory (when not given: jobs-action/store under the user cache directory)")
	fs.BoolVar(&o.list, "list", false, "print the tests that would run, and exit")
	fs.BoolVar(&o.version, "version", false, "print the version, and exit")
	return fs, o
}

// parse reads the command line, then the environment for what it left out.
// It returns flag.ErrHelp for --help, after printing the usage to out.
func parse(args []string, getenv func(string) string, out io.Writer) (*opts, error) {
	fs, o := newFlags(out)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if o.server == "" {
		o.server = getenv("JOBS_SERVER")
	}
	if o.addrs == "" {
		o.addrs = getenv("JOBS_SERVER_ADDR")
	}
	// A secret pasted with its trailing newline should still work.
	o.server = strings.TrimSpace(o.server)
	return o, nil
}

func main() {
	o, err := parse(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		// The flag package has already printed its own errors; this one may
		// be ours, and saying it twice costs little.
		fmt.Fprintf(os.Stderr, "jobs-action: %v\n", err)
		os.Exit(2)
	}
	if o.version {
		fmt.Println(version)
		return
	}
	absRoot, err := filepath.Abs(o.root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jobs-action: %v\n", err)
		os.Exit(1)
	}
	if o.dataDir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "jobs-action: no --data-dir and no user cache directory: %v\n", err)
			os.Exit(1)
		}
		o.dataDir = filepath.Join(cache, "jobs-action", "store")
	}

	// The registration is never released. GitHub follows its first signal
	// with a second one a few seconds later, and that one must not kill the
	// process while it is closing its check runs; action.Run sets its own
	// deadline instead.
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	os.Exit(action.Main(ctx, action.Config{
		Server:   o.server,
		Addrs:    action.SplitAddrs(o.addrs),
		Platform: o.platform,
		Root:     absRoot,
		DataDir:  o.dataDir,
		List:     o.list,
		Env:      action.EnvFromOS(),
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
	}))
}
