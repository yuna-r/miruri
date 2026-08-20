package inspect

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/yuna-r/miruri/internal/target"
)

func TestInspectNativeExecutable(t *testing.T) {
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang is not available")
	}
	root := t.TempDir()
	source := filepath.Join(root, "main.c")
	binary := filepath.Join(root, "sample")
	if err := os.WriteFile(source, []byte("int main(void){return 0;}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(clang, source, "-o", binary)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clang failed: %v\n%s", err, output)
	}
	profile, err := target.Resolve("host")
	if err != nil {
		t.Fatal(err)
	}
	artifact, recognized, err := InspectFile(binary, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !recognized {
		t.Fatal("native executable was not recognized")
	}
	if !artifact.ArchitectureOK {
		t.Fatalf("unexpected architecture %s for target %s", artifact.Architecture, profile.Arch)
	}
}
