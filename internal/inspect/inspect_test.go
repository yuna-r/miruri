package inspect

import (
	"os"
	"path/filepath"
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

func TestCollectAndPackagePreservesMacOSAppBundle(t *testing.T) {
	searchRoot := t.TempDir()
	packageRoot := t.TempDir()
	appRoot := filepath.Join(searchRoot, "MarbleMaze.app")
	executable := filepath.Join(appRoot, "Contents", "MacOS", "MarbleMaze")
	resource := filepath.Join(appRoot, "Contents", "Resources", "Media", "Models", "maze1.sdkmesh")
	plist := filepath.Join(appRoot, "Contents", "Info.plist")
	for _, path := range []string{executable, resource, plist} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Minimal 64-bit little-endian Mach-O header: MH_MAGIC_64, ARM64, MH_EXECUTE.
	macho := []byte{
		0xcf, 0xfa, 0xed, 0xfe,
		0x0c, 0x00, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x00,
		0x02, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
	if err := os.WriteFile(executable, macho, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resource, []byte("sdkmesh-payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, []byte("<?xml version=\"1.0\"?><plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile, err := target.Resolve("macos-arm64")
	if err != nil {
		t.Fatal(err)
	}
	result, err := CollectAndPackage(searchRoot, packageRoot, profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 1 {
		t.Fatalf("artifacts = %d, want 1: %+v", len(result.Artifacts), result.Artifacts)
	}
	for _, rel := range []string{
		"artifacts/MarbleMaze.app/Contents/MacOS/MarbleMaze",
		"artifacts/MarbleMaze.app/Contents/Info.plist",
		"artifacts/MarbleMaze.app/Contents/Resources/Media/Models/maze1.sdkmesh",
	} {
		if _, err := os.Stat(filepath.Join(packageRoot, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("missing bundled path %s: %v", rel, err)
		}
	}
}
