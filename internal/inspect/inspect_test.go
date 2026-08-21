package inspect

import (
	"os"
	"testing"

	"github.com/yuna-r/miruri/internal/target"
)

func TestInspectNativeExecutable(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
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
		t.Fatalf("native Go test executable was not recognized: %s", binary)
	}
	if !artifact.ArchitectureOK {
		t.Fatalf("unexpected architecture %s for target %s", artifact.Architecture, profile.Arch)
	}
}
