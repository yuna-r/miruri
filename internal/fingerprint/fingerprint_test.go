package fingerprint

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestProjectFingerprintIsDeterministicAndContentSensitive(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "src", "main.c")
	if err := os.WriteFile(path, []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := Project(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Now().Add(-time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	second, err := Project(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest {
		t.Fatalf("mtime changed fingerprint: %s != %s", first.Digest, second.Digest)
	}
	if err := os.WriteFile(path, []byte("int main(void) { return 1; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := Project(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if third.Digest == first.Digest {
		t.Fatal("content change did not change fingerprint")
	}
}

func TestProjectFingerprintExcludesGeneratedDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.c"), []byte("int x;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := Project(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "dist", "host"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "host", "artifact"), []byte("generated"), 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := Project(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest {
		t.Fatalf("generated dist changed fingerprint: %s != %s", first.Digest, second.Digest)
	}
}

func TestProjectFingerprintExcludesExplicitGeneratedPath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(root, "custom-results")
	if err := os.MkdirAll(generated, 0o755); err != nil {
		t.Fatal(err)
	}
	generatedFile := filepath.Join(generated, "report.json")
	if err := os.WriteFile(generatedFile, []byte(`{"run":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	withGenerated, err := Project(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	excludedFirst, err := Project(root, Options{ExcludePaths: []string{generated}})
	if err != nil {
		t.Fatal(err)
	}
	if withGenerated.Digest == excludedFirst.Digest {
		t.Fatal("explicit generated path did not affect the unfiltered fingerprint fixture")
	}
	if err := os.WriteFile(generatedFile, []byte(`{"run":2,"changed":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	excludedSecond, err := Project(root, Options{ExcludePaths: []string{generated}})
	if err != nil {
		t.Fatal(err)
	}
	if excludedFirst.Digest != excludedSecond.Digest || excludedFirst.FileCount != excludedSecond.FileCount {
		t.Fatalf("excluded output changed fingerprint: first=%+v second=%+v", excludedFirst, excludedSecond)
	}
}

func TestProjectFingerprintAcceptsSymlinkedProjectRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated privileges on Windows")
	}
	root := t.TempDir()
	realProject := filepath.Join(root, "real-project")
	if err := os.MkdirAll(realProject, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realProject, "main.c"), []byte("int main(void) { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "project-alias")
	if err := os.Symlink(realProject, alias); err != nil {
		t.Fatal(err)
	}
	direct, err := Project(realProject, Options{})
	if err != nil {
		t.Fatal(err)
	}
	throughAlias, err := Project(alias, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if direct.Digest != throughAlias.Digest || direct.FileCount != throughAlias.FileCount {
		t.Fatalf("symlinked project root changed fingerprint: direct=%+v alias=%+v", direct, throughAlias)
	}
}

func TestProjectFingerprintLengthFramesEntryPayloads(t *testing.T) {
	oneFile := t.TempDir()
	twoFiles := t.TempDir()
	// Under the previous delimiter-only encoding, this payload could absorb the
	// complete header and payload of a second file and produce the same byte
	// stream as the two-file tree below.
	ambiguousPayload := []byte{'X', 0, 'F', 0, 'b', 0, '0', 0, 'Y'}
	if err := os.WriteFile(filepath.Join(oneFile, "a"), ambiguousPayload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(twoFiles, "a"), []byte{'X'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(twoFiles, "b"), []byte{'Y'}, 0o644); err != nil {
		t.Fatal(err)
	}
	left, err := Project(oneFile, Options{})
	if err != nil {
		t.Fatal(err)
	}
	right, err := Project(twoFiles, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if left.Digest == right.Digest {
		t.Fatalf("length-framed fingerprints collided: left=%+v right=%+v", left, right)
	}
}
