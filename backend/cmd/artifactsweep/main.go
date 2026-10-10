// Command artifactsweep is the operator's offline sweeper for Execution Artifact objects
// that no committed metadata row references.
//
// A Tool saves an artifact's bytes during its call, before the result transaction that
// commits the artifact's metadata row. When that transaction never commits -- it rolled
// back, its Attempt was already closed, or the process crashed -- the object stays on disk
// unreferenced. The Runtime never deletes it, because the Runtime cannot tell an orphan
// from an object whose row is about to commit. This command can, given a grace period
// longer than any Tool call: an object older than the grace period with no row is an
// orphan. Staging files older than the grace period are leftovers of interrupted writes.
//
// It is a dry run unless --delete is given, and it prints counts and artifact IDs only,
// never a storage key or path.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
)

// defaultGrace is how old an unreferenced object must be before it counts as an orphan.
// It is far longer than any Agent Run deadline, so no Tool call that saved the object
// can still be about to commit its row.
const defaultGrace = 24 * time.Hour

// enumerationMaxBytes is the content cap handed to the store. The sweeper never writes,
// so any positive value is equivalent.
const enumerationMaxBytes = 1

// Exit codes: a usage or configuration error is distinguished from a failed sweep.
const (
	exitOK    = 0
	exitSweep = 1
	exitUsage = 2
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr, time.Now)
	stop()
	os.Exit(code)
}

// options is the parsed command line and environment.
type options struct {
	grace       time.Duration
	delete      bool
	databaseURL string
	storageRoot string
}

func parseOptions(args []string, getenv func(string) string, stderr io.Writer) (options, error) {
	flags := flag.NewFlagSet("artifactsweep", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var opts options
	flags.DurationVar(&opts.grace, "grace", defaultGrace, "minimum age of an unreferenced object or staging file before it is swept")
	flags.BoolVar(&opts.delete, "delete", false, "delete what is found; without it the sweep only reports")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if opts.grace <= 0 {
		return options{}, errors.New("--grace must be positive")
	}
	opts.databaseURL = getenv(config.KeyDatabaseURL)
	if opts.databaseURL == "" {
		return options{}, errors.New(config.KeyDatabaseURL + " is required")
	}
	opts.storageRoot = getenv(config.KeyAssetStorageRoot)
	if opts.storageRoot == "" {
		return options{}, errors.New(config.KeyAssetStorageRoot + " is required")
	}
	return opts, nil
}

// run is main without the process: it returns the exit code instead of exiting.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, now func() time.Time) int {
	opts, err := parseOptions(args, getenv, stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprintf(stderr, "artifactsweep: %v\n", err)
		}
		return exitUsage
	}

	out := &reportWriter{w: stdout}

	poolCfg, err := pgxpool.ParseConfig(opts.databaseURL)
	if err != nil {
		// The parse error can quote the connection string, which carries the password.
		_, _ = fmt.Fprintf(stderr, "artifactsweep: %s is not a valid connection string\n", config.KeyDatabaseURL)
		return exitUsage
	}
	poolCfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artifactsweep: open database pool: %v\n", err)
		return exitSweep
	}
	defer pool.Close()

	sweeper := sweeper{
		uow:    postgres.NewUnitOfWork(pool),
		store:  asset.NewArtifactStore(opts.storageRoot, enumerationMaxBytes),
		cutoff: now().Add(-opts.grace),
		delete: opts.delete,
		out:    out,
	}
	mode := "dry-run"
	if opts.delete {
		mode = "delete"
	}
	out.printf("mode: %s\ngrace: %s\n", mode, opts.grace)
	report, err := sweeper.sweep(ctx)
	report.print(out)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "artifactsweep: %v\n", err)
		return exitSweep
	}
	if out.err != nil {
		_, _ = fmt.Fprintf(stderr, "artifactsweep: write report: %v\n", out.err)
		return exitSweep
	}
	return exitOK
}

// reportWriter prints report lines and keeps the first write error, so a sweep whose
// report was lost -- including the IDs of what it deleted -- exits non-zero.
type reportWriter struct {
	w   io.Writer
	err error
}

func (r *reportWriter) printf(format string, args ...any) {
	if r.err == nil {
		_, r.err = fmt.Fprintf(r.w, format, args...)
	}
}

// sweeper finds and optionally removes unreferenced artifact objects and staging
// leftovers older than cutoff.
type sweeper struct {
	uow    store.UnitOfWork
	store  *asset.ArtifactStore
	cutoff time.Time
	delete bool
	out    *reportWriter
}

// sweepReport is what one sweep found. Every count is of objects, never bytes.
type sweepReport struct {
	scanned        int
	recent         int
	referenced     int
	orphaned       int
	deleted        int
	staged         int
	stagedDeleted  int
	refreshedSince int
}

func (r sweepReport) print(w *reportWriter) {
	w.printf("objects scanned: %d\n", r.scanned)
	w.printf("objects within grace: %d\n", r.recent)
	w.printf("objects referenced: %d\n", r.referenced)
	w.printf("orphaned objects: %d\n", r.orphaned)
	w.printf("orphans refreshed before removal: %d\n", r.refreshedSince)
	w.printf("objects deleted: %d\n", r.deleted)
	w.printf("staged leftovers past grace: %d\n", r.staged)
	w.printf("staged leftovers deleted: %d\n", r.stagedDeleted)
}

func (s sweeper) sweep(ctx context.Context) (sweepReport, error) {
	var report sweepReport
	objects, err := s.store.Objects(ctx)
	if err != nil {
		return report, fmt.Errorf("list artifact objects: %w", err)
	}
	for _, object := range objects {
		report.scanned++
		if !object.ModTime.Before(s.cutoff) {
			report.recent++
			continue
		}
		referenced, err := s.referenced(ctx, object.ArtifactID)
		if err != nil {
			return report, err
		}
		if referenced {
			report.referenced++
			continue
		}
		report.orphaned++
		if !s.delete {
			s.out.printf("orphan: %s\n", object.ArtifactID)
			continue
		}
		// The age is checked again at removal: identical bytes saved since enumeration
		// restart it, and that save's row may be about to commit.
		removed, err := s.store.RemoveIfOlder(object.StorageKey, s.cutoff)
		if err != nil {
			return report, fmt.Errorf("remove orphan %s: %w", object.ArtifactID, err)
		}
		if !removed {
			report.refreshedSince++
			continue
		}
		report.deleted++
		s.out.printf("deleted: %s\n", object.ArtifactID)
	}

	leftovers, err := s.store.StagedLeftovers(ctx)
	if err != nil {
		return report, fmt.Errorf("list staged leftovers: %w", err)
	}
	for _, leftover := range leftovers {
		if !leftover.ModTime.Before(s.cutoff) {
			continue
		}
		report.staged++
		if !s.delete {
			continue
		}
		if err := s.store.RemoveStaged(leftover.Name); err != nil {
			return report, fmt.Errorf("remove staged leftover: %w", err)
		}
		report.stagedDeleted++
	}
	return report, nil
}

// referenced reports whether an execution_artifacts row names artifactID. Asset objects
// live outside the artifact subtree and are never enumerated here, so the artifact
// metadata table is the only possible reference.
func (s sweeper) referenced(ctx context.Context, artifactID string) (bool, error) {
	var found bool
	err := s.uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.Artifacts().Get(ctx, artifactID)
		switch {
		case err == nil:
			found = true
			return nil
		case errors.Is(err, domain.ErrNotFound):
			return nil
		default:
			return err
		}
	})
	if err != nil {
		return false, fmt.Errorf("look up artifact %s: %w", artifactID, err)
	}
	return found, nil
}
