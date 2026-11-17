package action

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jobs-build/assimilate/builds"
	"github.com/jobs-build/assimilate/jobs"

	"github.com/jobs-build/jobs-action/checks"
)

// Main runs the command against the real world: assimilate's local store at
// cfg.DataDir, a jobs-iroh server, and GitHub's API when in GitHub Actions.
func Main(ctx context.Context, cfg Config) int {
	// The libraries underneath log through the standard logger (slog's
	// default handler included), and their lines can name the peer. Send them
	// through the same scrubbing as everything else.
	if cfg.Server != "" {
		prev := log.Writer()
		log.SetOutput(&lineWriter{w: cfg.Stderr, scrub: newScrubber(cfg.Server)})
		defer log.SetOutput(prev)
	}

	local, err := jobs.Open(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "jobs-action: open the local store: %v\n", err)
		return 1
	}
	d := Deps{
		Discover: local.TestDirs,
		Dial: func(ctx context.Context) (builds.Backend, func(), error) {
			client, err := jobs.Dial(ctx, local, jobs.Options{Server: cfg.Server, Addrs: cfg.Addrs})
			if err != nil {
				return nil, nil, err
			}
			return client, func() { _ = client.Close() }, nil
		},
		Now:   time.Now,
		Grace: DefaultGrace,
	}
	if cfg.Env.Actions {
		apiURL := cfg.Env.APIURL
		if apiURL == "" {
			apiURL = "https://api.github.com"
		}
		d.API = &checks.Client{BaseURL: apiURL, Repo: cfg.Env.Repository, Token: cfg.Env.Token}
	}
	code := Run(ctx, cfg, d)
	// After a cancellation the run may still hold the store, and the process
	// is about to be killed anyway: closing could only block.
	if ctx.Err() == nil {
		_ = local.Close()
	}
	return code
}
