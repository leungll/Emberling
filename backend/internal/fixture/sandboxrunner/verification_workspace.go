package sandboxrunner

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// A verification works on a throwaway checkout: the base commit's tree exported from a
// registered local repository with `git archive`, with the patch applied on top. The
// repository itself is only ever read; nothing is checked out, fetched or committed in it.
// Both verification backends prepare the checkout the same way, on the runner's host, and
// differ only in where the commands then run.

// maxCheckoutEntryBytes bounds one file of an exported tree.
const maxCheckoutEntryBytes = 64 << 20

// maxGitDiagnosticBytes bounds the git diagnostics a verification log keeps.
const maxGitDiagnosticBytes = 4 << 10

// Permissions of the throwaway checkout: it belongs to this process alone.
const (
	checkoutDirMode  = 0o700
	checkoutFileMode = 0o600
	checkoutExecMode = 0o700
)

// Module files a patch may change and the verification must leave untouched.
var moduleFiles = []string{"go.mod", "go.sum"}

var (
	errUnknownRepository = errors.New("repository is not registered")
	errUnknownCommit     = errors.New("commit is not in the repository")
)

// LocalRepositories exports commits of local git repositories registered by identifier.
type LocalRepositories struct {
	paths map[string]string
}

// NewLocalRepositories returns the repositories at the given paths, keyed by identifier.
func NewLocalRepositories(paths map[string]string) *LocalRepositories {
	copied := make(map[string]string, len(paths))
	for id, p := range paths {
		copied[id] = p
	}
	return &LocalRepositories{paths: copied}
}

// Archive returns the tar of the tree at commit, as `git archive` writes it.
func (r *LocalRepositories) Archive(ctx context.Context, repositoryID, commit string) ([]byte, error) {
	repoPath, ok := r.paths[repositoryID]
	if !ok {
		return nil, errUnknownRepository
	}
	var out, stderr bytes.Buffer
	// Every argument is fixed or the registered path; the commit was validated as a
	// request member and is passed as one argument after the fixed verb.
	cmd := gitCommand(ctx, repoPath, "-C", repoPath, "archive", "--format=tar", commit)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %s", errUnknownCommit, boundedText(stderr.Bytes(), maxGitDiagnosticBytes))
	}
	return out.Bytes(), nil
}

// gitCommand builds a git invocation isolated from the host's user and system
// configuration, hooks and credential prompts, running in dir.
func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	base := []string{
		"-c", "core.autocrlf=false", "-c", "core.hooksPath=/dev/null", "-c", "diff.renames=false",
	}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...) //nolint:gosec // G204: fixed executable, arguments assembled by this package from registered paths and validated request members, no shell involved
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
	)
	return cmd
}

// extractTar unpacks a tar stream into dir. Entry names are checked to stay inside dir.
func extractTar(r io.Reader, dir string) error {
	reader := tar.NewReader(r)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == "." || name == "" {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("archive entry %q escapes the checkout", header.Name)
		}
		target := filepath.Join(dir, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, checkoutDirMode); err != nil {
				return fmt.Errorf("create directory: %w", err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), checkoutDirMode); err != nil {
				return fmt.Errorf("create directory: %w", err)
			}
			mode := os.FileMode(checkoutFileMode)
			if header.Mode&0o111 != 0 {
				mode = checkoutExecMode
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return fmt.Errorf("create file: %w", err)
			}
			written, err := io.Copy(f, io.LimitReader(reader, maxCheckoutEntryBytes+1))
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return fmt.Errorf("write file: %w", err)
			}
			if written > maxCheckoutEntryBytes {
				return fmt.Errorf("archive entry %q is over the %d-byte bound", header.Name, maxCheckoutEntryBytes)
			}
		default:
			// Symbolic links, hard links and devices are not part of a module a patch
			// applies to and are left out of the checkout.
		}
	}
}

// checkout is the prepared throwaway tree of one verification.
type checkout struct {
	dir string
	// moduleFiles holds go.mod and go.sum as they were right after the patch applied, so
	// the backend can prove the verification left them unchanged.
	moduleFiles map[string][]byte
	// log is the preparation output kept for the result log.
	log bytes.Buffer
}

// prepareCheckout exports the base commit into a new temporary directory and applies the
// patch. A failure outcome names why no checkout exists; the caller removes dir in every
// case where one is returned.
func prepareCheckout(ctx context.Context, repos *LocalRepositories, job VerificationJob) (*checkout, *VerificationOutcome) {
	archive, err := repos.Archive(ctx, job.RepositoryID, job.BaseCommit)
	switch {
	case errors.Is(err, errUnknownRepository):
		return nil, &VerificationOutcome{FailureCode: codeUnknownRepository, FailureMessage: "repository is not registered with the sandbox runner"}
	case errors.Is(err, errUnknownCommit):
		return nil, &VerificationOutcome{FailureCode: codeUnknownBaseCommit, FailureMessage: "base commit is not in the registered repository"}
	case err != nil:
		return nil, &VerificationOutcome{FailureCode: codeSandboxError, FailureMessage: "base commit could not be exported"}
	}
	dir, err := os.MkdirTemp("", "emberling-verify-")
	if err != nil {
		return nil, &VerificationOutcome{FailureCode: codeSandboxError, FailureMessage: "checkout directory could not be created"}
	}
	c := &checkout{dir: dir, moduleFiles: make(map[string][]byte)}
	if err := extractTar(bytes.NewReader(archive), dir); err != nil {
		c.remove()
		return nil, &VerificationOutcome{FailureCode: codeSandboxError, FailureMessage: "base commit could not be unpacked"}
	}
	fmt.Fprintf(&c.log, "$ git archive %s (repository %s)\n", job.BaseCommit, job.RepositoryID)
	if job.Patch != "" {
		var output bytes.Buffer
		cmd := gitCommand(ctx, dir, "apply", "--whitespace=nowarn", "--verbose", "-")
		cmd.Stdin = strings.NewReader(job.Patch)
		cmd.Stdout, cmd.Stderr = &output, &output
		err := cmd.Run()
		fmt.Fprintf(&c.log, "$ git apply --whitespace=nowarn\n%s", boundedText(output.Bytes(), maxGitDiagnosticBytes))
		if err != nil {
			c.remove()
			if ctx.Err() != nil {
				return nil, &VerificationOutcome{FailureCode: codeSandboxError, FailureMessage: "verification was interrupted"}
			}
			return nil, &VerificationOutcome{FailureCode: codePatchRejected, FailureMessage: "patch does not apply to the base commit", Log: c.log.Bytes()}
		}
	} else {
		c.log.WriteString("(empty patch: verifying the base commit itself)\n")
	}
	for _, name := range moduleFiles {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		c.moduleFiles[name] = content
	}
	return c, nil
}

// moduleFilesUnchanged reports whether go.mod and go.sum still hold what they held right
// after the patch applied.
func (c *checkout) moduleFilesUnchanged() bool {
	for _, name := range moduleFiles {
		content, err := os.ReadFile(filepath.Join(c.dir, name))
		before, had := c.moduleFiles[name]
		if had != (err == nil) || !bytes.Equal(before, content) {
			return false
		}
	}
	return true
}

// archive returns the checkout as a tar, in sorted path order, for streaming into a
// container.
func (c *checkout) archive() ([]byte, error) {
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	root, err := os.OpenRoot(c.dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	err = filepath.WalkDir(c.dir, func(p string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(c.dir, p)
		if err != nil || rel == "." {
			return err
		}
		name := filepath.ToSlash(rel)
		if entry.IsDir() {
			return writer.WriteHeader(&tar.Header{Name: name + "/", Mode: 0o755, Typeflag: tar.TypeDir})
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		// The file is read through the checkout root, so a link planted in the tree
		// cannot lead the read outside it.
		f, err := root.Open(rel)
		if err != nil {
			return err
		}
		content, err := io.ReadAll(io.LimitReader(f, maxCheckoutEntryBytes))
		_ = f.Close()
		if err != nil {
			return err
		}
		mode := int64(0o644)
		if info, err := entry.Info(); err == nil && info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = writer.Write(content)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (c *checkout) remove() {
	_ = os.RemoveAll(c.dir)
}

// boundedText renders command output for a log or an error, cut to limit bytes.
func boundedText(output []byte, limit int) string {
	text := strings.TrimSpace(string(output))
	if len(text) > limit {
		text = text[:limit] + "..."
	}
	if text == "" {
		return ""
	}
	return text + "\n"
}
