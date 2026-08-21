package repairworkspace

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Repository is a disposable Git repository created inside Miruri's isolated
// source overlay. It exists only to capture exact Codex repair patches and is
// never the user's original repository.
type Repository struct {
	dir     string
	gitPath string
}

type DiscardedChange struct {
	Path   string
	Reason string
}

type ChangeFilter func(path string) (keep bool, reason string)

type CaptureOptions struct {
	Filter ChangeFilter
}

type ChangeSet struct {
	Files     []string
	Patch     []byte
	Discarded []DiscardedChange
}

func Init(dir string) (*Repository, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git is required for Codex repair isolation: %w", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("repair workspace is not a directory: %s", abs)
	}
	// CopyTree intentionally excludes Git metadata, but also remove a copied
	// top-level .git file defensively before creating the disposable repository.
	if err := os.RemoveAll(filepath.Join(abs, ".git")); err != nil {
		return nil, fmt.Errorf("remove inherited Git metadata: %w", err)
	}
	r := &Repository{dir: abs, gitPath: gitPath}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "Miruri Repair Bot"},
		{"config", "user.email", "miruri@local.invalid"},
		{"config", "core.autocrlf", "false"},
		{"config", "core.filemode", "true"},
	} {
		if _, err := r.run(args...); err != nil {
			return nil, err
		}
	}
	if err := r.stageAll(); err != nil {
		return nil, err
	}
	if _, err := r.run("commit", "-q", "--allow-empty", "-m", "Miruri isolated source baseline"); err != nil {
		return nil, err
	}
	return r, nil
}

// CaptureAndCommit stages every change, returns a binary-safe patch relative to
// the previous checkpoint, and commits the new checkpoint for a later repair.
func (r *Repository) CaptureAndCommit(message string) (ChangeSet, error) {
	return r.CaptureAndCommitWithOptions(message, CaptureOptions{})
}

// CaptureAndCommitWithOptions can discard build products or policy-forbidden
// files before the authoritative repair patch is created. Discarded paths are
// restored to the previous checkpoint (or removed when newly created).
func (r *Repository) CaptureAndCommitWithOptions(message string, options CaptureOptions) (ChangeSet, error) {
	if err := r.stageAll(); err != nil {
		return ChangeSet{}, err
	}
	namesRaw, err := r.runBytes("diff", "--cached", "--name-only", "-z", "HEAD", "--", ".")
	if err != nil {
		return ChangeSet{}, err
	}
	files := parseNULList(namesRaw)
	var discarded []DiscardedChange
	if options.Filter != nil {
		for _, path := range files {
			keep, reason := options.Filter(path)
			if keep {
				continue
			}
			if strings.TrimSpace(reason) == "" {
				reason = "excluded by repair patch policy"
			}
			if err := r.discardPath(path); err != nil {
				return ChangeSet{}, fmt.Errorf("discard repair change %s: %w", path, err)
			}
			discarded = append(discarded, DiscardedChange{Path: path, Reason: reason})
		}
		if err := r.stageAll(); err != nil {
			return ChangeSet{}, err
		}
	}
	patch, err := r.runBytes("diff", "--cached", "--binary", "--no-ext-diff", "HEAD", "--", ".")
	if err != nil {
		return ChangeSet{}, err
	}
	namesRaw, err = r.runBytes("diff", "--cached", "--name-only", "-z", "HEAD", "--", ".")
	if err != nil {
		return ChangeSet{}, err
	}
	files = parseNULList(namesRaw)
	if len(files) == 0 {
		return ChangeSet{Discarded: discarded}, nil
	}
	if _, err := r.run("commit", "-q", "-m", message); err != nil {
		return ChangeSet{}, err
	}
	return ChangeSet{Files: files, Patch: patch, Discarded: discarded}, nil
}

func (r *Repository) discardPath(path string) error {
	path = filepath.Clean(path)
	if path == "." || filepath.IsAbs(path) || strings.HasPrefix(path, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("unsafe repair path %q", path)
	}
	// Tracked paths can be restored directly from HEAD, including deletions.
	if _, err := r.run("checkout", "-q", "HEAD", "--", path); err == nil {
		return nil
	}
	// The path did not exist in HEAD. Remove it from the temporary index and
	// filesystem. This also handles ignored build products staged with -f.
	_, _ = r.run("reset", "-q", "HEAD", "--", path)
	return os.RemoveAll(filepath.Join(r.dir, path))
}

// Head returns the current checkpoint commit. Callers can retain it before an
// external repair and use ResetTo when the repair must be discarded.
func (r *Repository) Head() (string, error) {
	output, err := r.run("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

// Reset discards an unsuccessful Codex attempt and restores the previous
// checkpoint without touching anything outside the isolated overlay.
func (r *Repository) Reset() error {
	return r.ResetTo("HEAD")
}

// ResetTo restores a retained checkpoint, including tracked and untracked
// workspace changes, without touching anything outside the isolated overlay.
func (r *Repository) ResetTo(ref string) error {
	if strings.TrimSpace(ref) == "" {
		return fmt.Errorf("Git checkpoint reference is empty")
	}
	if _, err := r.run("reset", "--hard", "-q", ref); err != nil {
		return err
	}
	if _, err := r.run("clean", "-fdxq"); err != nil {
		return err
	}
	return nil
}

func (r *Repository) stageAll() error {
	// -f is intentional: ignored source files can still be semantically required
	// by an existing project and must be represented in a complete repair patch.
	_, err := r.run("add", "-A", "-f", "--", ".")
	return err
}

func (r *Repository) run(args ...string) (string, error) {
	output, err := r.runBytes(args...)
	return string(output), err
}

func (r *Repository) runBytes(args ...string) ([]byte, error) {
	command := exec.Command(r.gitPath, args...)
	command.Dir = r.dir
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func parseNULList(data []byte) []string {
	parts := bytes.Split(data, []byte{0})
	files := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		files = append(files, filepath.ToSlash(string(part)))
	}
	sort.Strings(files)
	return files
}
