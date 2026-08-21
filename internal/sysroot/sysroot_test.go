package sysroot

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yuna-r/miruri/internal/model"
)

type archiveEntry struct {
	name     string
	body     string
	typeflag byte
	linkname string
	mode     int64
}

type fakeOCI struct {
	server       *httptest.Server
	image        string
	manifestHits atomic.Int64
	blobHits     atomic.Int64
	blobs        map[string][]byte
}

func TestManagerEnsuresLocksAndReusesManagedSysroot(t *testing.T) {
	fixture := newFakeOCI(t, false)
	cache := t.TempDir()
	provider := Provider{
		ID:           "test-provider",
		TargetID:     "linux-x86_64",
		Image:        fixture.image,
		OS:           "linux",
		Architecture: "amd64",
		Description:  "test",
	}
	manager := New(Options{
		CacheDir:       cache,
		HTTPClient:     fixture.server.Client(),
		RegistryScheme: "http",
		Providers:      []Provider{provider},
	})
	profile := model.TargetProfile{ID: "linux-x86_64", OS: "linux", Arch: "x86_64", RequiresSysroot: true}
	resolution, err := manager.Ensure(context.Background(), profile, EnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Mode != "managed" || resolution.ManifestDigest == "" || resolution.LockFile == "" {
		t.Fatalf("unexpected resolution: %+v", resolution)
	}
	for _, relative := range []string{
		"usr/include/stdio.h",
		"usr/lib/x86_64-linux-gnu/crt1.o",
		"usr/lib/x86_64-linux-gnu/libc.so",
		"usr/lib/gcc/x86_64-linux-gnu/12/libgcc.a",
		"usr/bin/new-tool",
	} {
		if _, err := os.Stat(filepath.Join(resolution.Path, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("missing extracted %s: %v", relative, err)
		}
	}
	if _, err := os.Stat(filepath.Join(resolution.Path, "usr", "bin", "old-tool")); !os.IsNotExist(err) {
		t.Fatalf("OCI whiteout did not remove old-tool: %v", err)
	}
	link, err := os.Readlink(filepath.Join(resolution.Path, "lib64"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(link) || link != filepath.FromSlash("usr/lib/x86_64-linux-gnu") {
		t.Fatalf("absolute image symlink was not rewritten inside rootfs: %q", link)
	}
	var lock Lock
	if err := readJSON(resolution.LockFile, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.SchemaVersion != lockSchema || lock.ManifestDigest != resolution.ManifestDigest || len(lock.Layers) != 2 {
		t.Fatalf("unexpected lock: %+v", lock)
	}
	listed, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Path != resolution.Path {
		t.Fatalf("unexpected sysroot list: %+v", listed)
	}

	manifestHits := fixture.manifestHits.Load()
	blobHits := fixture.blobHits.Load()
	fixture.server.Close()
	cached, err := manager.Ensure(context.Background(), profile, EnsureOptions{Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	if cached.Path != resolution.Path || cached.ManifestDigest != resolution.ManifestDigest {
		t.Fatalf("cache lookup changed resolution: %+v", cached)
	}
	if fixture.manifestHits.Load() != manifestHits || fixture.blobHits.Load() != blobHits {
		t.Fatal("offline cache reuse contacted the registry")
	}
	if err := manager.Remove(profile.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(resolution.Path)); !os.IsNotExist(err) {
		t.Fatalf("purge did not remove unreferenced content store: %v", err)
	}
}

func TestManagerOfflineWithoutCacheFails(t *testing.T) {
	provider := Provider{ID: "test", TargetID: "linux-x86_64", Image: "example.invalid/test:tag", OS: "linux", Architecture: "amd64"}
	manager := New(Options{CacheDir: t.TempDir(), Providers: []Provider{provider}})
	profile := model.TargetProfile{ID: "linux-x86_64", OS: "linux", Arch: "x86_64", RequiresSysroot: true}
	_, err := manager.Ensure(context.Background(), profile, EnsureOptions{Offline: true})
	if err == nil || !strings.Contains(err.Error(), "--offline") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestManagerRejectsBlobDigestMismatch(t *testing.T) {
	fixture := newFakeOCI(t, true)
	defer fixture.server.Close()
	provider := Provider{ID: "test", TargetID: "linux-x86_64", Image: fixture.image, OS: "linux", Architecture: "amd64"}
	manager := New(Options{CacheDir: t.TempDir(), HTTPClient: fixture.server.Client(), RegistryScheme: "http", Providers: []Provider{provider}})
	profile := model.TargetProfile{ID: "linux-x86_64", OS: "linux", Arch: "x86_64", RequiresSysroot: true}
	_, err := manager.Ensure(context.Background(), profile, EnsureOptions{})
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
}

func TestApplyLayerRejectsPathTraversal(t *testing.T) {
	layer := tarGzip(t, []archiveEntry{{name: "../../escape", body: "owned", typeflag: tar.TypeReg, mode: 0o644}})
	blob := filepath.Join(t.TempDir(), "layer.tar.gz")
	if err := os.WriteFile(blob, layer, 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := applyLayer(root, blob, "application/vnd.oci.image.layer.v1.tar+gzip"); err == nil {
		t.Fatal("path traversal layer was accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape")); !os.IsNotExist(err) {
		t.Fatalf("path traversal wrote outside root: %v", err)
	}
}

func TestEnsureParentWithinRootHandlesSymlinkedRootPrefix(t *testing.T) {
	realParent := t.TempDir()
	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "alias")
	if err := os.Symlink(realParent, alias); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(alias, "rootfs")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "usr", "include", "stdio.h")
	if err := ensureParentWithinRoot(root, destination); err != nil {
		t.Fatalf("symlinked root prefix was rejected: %v", err)
	}
}

func TestEnsureParentWithinRootRejectsEscapingAncestorSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "usr")); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "usr", "include", "stdio.h")
	if err := ensureParentWithinRoot(root, destination); err == nil {
		t.Fatal("escaping ancestor symlink was accepted")
	}
}

func TestApplyLayerRejectsEscapingRelativeSymlink(t *testing.T) {
	layer := tarGzip(t, []archiveEntry{{name: "usr/link", typeflag: tar.TypeSymlink, linkname: "../../outside", mode: 0o777}})
	blob := filepath.Join(t.TempDir(), "layer.tar.gz")
	if err := os.WriteFile(blob, layer, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyLayer(t.TempDir(), blob, "application/vnd.oci.image.layer.v1.tar+gzip"); err == nil {
		t.Fatal("escaping symlink was accepted")
	}
}

func newFakeOCI(t *testing.T, corruptLayer bool) *fakeOCI {
	t.Helper()
	layer1 := tarGzip(t, []archiveEntry{
		{name: "usr/include/stdio.h", body: "/* fake stdio */\n", typeflag: tar.TypeReg, mode: 0o644},
		{name: "usr/lib/x86_64-linux-gnu/crt1.o", body: "crt", typeflag: tar.TypeReg, mode: 0o644},
		{name: "usr/lib/x86_64-linux-gnu/libc.so", body: "libc", typeflag: tar.TypeReg, mode: 0o644},
		{name: "usr/lib/gcc/x86_64-linux-gnu/12/libgcc.a", body: "libgcc", typeflag: tar.TypeReg, mode: 0o644},
		{name: "usr/bin/old-tool", body: "old", typeflag: tar.TypeReg, mode: 0o755},
		{name: "lib64", typeflag: tar.TypeSymlink, linkname: "/usr/lib/x86_64-linux-gnu", mode: 0o777},
	})
	layer2 := tarGzip(t, []archiveEntry{
		{name: "usr/bin/.wh.old-tool", body: "", typeflag: tar.TypeReg, mode: 0o000},
		{name: "usr/bin/new-tool", body: "new", typeflag: tar.TypeReg, mode: 0o755},
	})
	config := mustJSON(t, imageConfig{Architecture: "amd64", OS: "linux"})
	configDescriptor := blobDescriptor(config, "application/vnd.oci.image.config.v1+json")
	layer1Descriptor := blobDescriptor(layer1, "application/vnd.oci.image.layer.v1.tar+gzip")
	layer2Descriptor := blobDescriptor(layer2, "application/vnd.oci.image.layer.v1.tar+gzip")
	manifest := mustJSON(t, imageManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config:        configDescriptor,
		Layers:        []descriptor{layer1Descriptor, layer2Descriptor},
	})
	manifestDigest := digestBytes(manifest)
	armConfig := mustJSON(t, imageConfig{Architecture: "arm64", OS: "linux"})
	armConfigDescriptor := blobDescriptor(armConfig, "application/vnd.oci.image.config.v1+json")
	armManifest := mustJSON(t, imageManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config:        armConfigDescriptor,
		Layers:        []descriptor{layer1Descriptor},
	})
	armManifestDigest := digestBytes(armManifest)
	index := mustJSON(t, manifestIndex{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.index.v1+json",
		Manifests: []descriptor{
			{MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: armManifestDigest, Size: int64(len(armManifest)), Platform: &platform{OS: "linux", Architecture: "arm64"}},
			{MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: manifestDigest, Size: int64(len(manifest)), Platform: &platform{OS: "linux", Architecture: "amd64"}},
		},
	})
	indexDigest := digestBytes(index)
	blobs := map[string][]byte{
		configDescriptor.Digest:    config,
		armConfigDescriptor.Digest: armConfig,
		layer1Descriptor.Digest:    layer1,
		layer2Descriptor.Digest:    layer2,
	}
	if corruptLayer {
		blobs[layer2Descriptor.Digest] = append(append([]byte(nil), layer2...), byte('x'))
	}
	fixture := &fakeOCI{blobs: blobs}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"token":"test-token"}`)
			return
		}
		if request.Header.Get("Authorization") != "Bearer test-token" {
			writer.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="test",scope="repository:test/sysroot:pull"`, server.URL))
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		const manifestPrefix = "/v2/test/sysroot/manifests/"
		const blobPrefix = "/v2/test/sysroot/blobs/"
		switch {
		case strings.HasPrefix(request.URL.Path, manifestPrefix):
			fixture.manifestHits.Add(1)
			reference := strings.TrimPrefix(request.URL.Path, manifestPrefix)
			var body []byte
			var mediaType, digest string
			switch reference {
			case "tag":
				body, mediaType, digest = index, "application/vnd.oci.image.index.v1+json", indexDigest
			case manifestDigest:
				body, mediaType, digest = manifest, "application/vnd.oci.image.manifest.v1+json", manifestDigest
			case armManifestDigest:
				body, mediaType, digest = armManifest, "application/vnd.oci.image.manifest.v1+json", armManifestDigest
			default:
				http.NotFound(writer, request)
				return
			}
			writer.Header().Set("Content-Type", mediaType)
			writer.Header().Set("Docker-Content-Digest", digest)
			_, _ = writer.Write(body)
		case strings.HasPrefix(request.URL.Path, blobPrefix):
			fixture.blobHits.Add(1)
			digest := strings.TrimPrefix(request.URL.Path, blobPrefix)
			body, ok := fixture.blobs[digest]
			if !ok {
				http.NotFound(writer, request)
				return
			}
			_, _ = writer.Write(body)
		default:
			http.NotFound(writer, request)
		}
	}))
	fixture.server = server
	fixture.image = strings.TrimPrefix(server.URL, "http://") + "/test/sysroot:tag"
	return fixture
}

func tarGzip(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		mode := entry.mode
		if mode == 0 && entry.typeflag != tar.TypeReg {
			mode = 0o755
		}
		header := &tar.Header{Name: entry.name, Typeflag: entry.typeflag, Linkname: entry.linkname, Mode: mode, Size: int64(len(entry.body))}
		if entry.typeflag == tar.TypeSymlink || entry.typeflag == tar.TypeLink || entry.typeflag == tar.TypeDir {
			header.Size = 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := io.WriteString(tarWriter, entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func blobDescriptor(body []byte, mediaType string) descriptor {
	return descriptor{MediaType: mediaType, Digest: digestBytes(body), Size: int64(len(body))}
}

func digestBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestApplyLayerWhiteoutsPreserveEntriesCreatedEarlierInSameLayer(t *testing.T) {
	root := t.TempDir()
	base := tarGzip(t, []archiveEntry{
		{name: "etc/obsolete", body: "old", typeflag: tar.TypeReg, mode: 0o644},
		{name: "etc/keep/lower", body: "lower", typeflag: tar.TypeReg, mode: 0o644},
		{name: "usr/bin/tool", body: "old", typeflag: tar.TypeReg, mode: 0o755},
	})
	basePath := filepath.Join(t.TempDir(), "base.tar.gz")
	if err := os.WriteFile(basePath, base, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyLayer(root, basePath, "application/vnd.oci.image.layer.v1.tar+gzip"); err != nil {
		t.Fatal(err)
	}

	upper := tarGzip(t, []archiveEntry{
		{name: "etc/current", body: "current", typeflag: tar.TypeReg, mode: 0o644},
		{name: "etc/keep/current", body: "current", typeflag: tar.TypeReg, mode: 0o644},
		{name: "usr/bin/tool", body: "new", typeflag: tar.TypeReg, mode: 0o755},
		{name: "usr/bin/.wh.tool", typeflag: tar.TypeReg},
		{name: "etc/.wh..wh..opq", typeflag: tar.TypeReg},
	})
	upperPath := filepath.Join(t.TempDir(), "upper.tar.gz")
	if err := os.WriteFile(upperPath, upper, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyLayer(root, upperPath, "application/vnd.oci.image.layer.v1.tar+gzip"); err != nil {
		t.Fatal(err)
	}

	for _, relative := range []string{"etc/current", "etc/keep/current", "usr/bin/tool"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("same-layer entry %s was removed by a later whiteout: %v", relative, err)
		}
	}
	for _, relative := range []string{"etc/obsolete", "etc/keep/lower"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); !os.IsNotExist(err) {
			t.Fatalf("lower-layer entry %s survived opaque whiteout: %v", relative, err)
		}
	}
	tool, err := os.ReadFile(filepath.Join(root, "usr", "bin", "tool"))
	if err != nil {
		t.Fatal(err)
	}
	if string(tool) != "new" {
		t.Fatalf("same-layer replacement changed unexpectedly: %q", tool)
	}
}

func TestManagerRevalidatesAndRefetchesCachedBlobs(t *testing.T) {
	fixture := newFakeOCI(t, false)
	defer fixture.server.Close()
	cache := t.TempDir()
	provider := Provider{ID: "test", TargetID: "linux-x86_64", Image: fixture.image, OS: "linux", Architecture: "amd64"}
	manager := New(Options{CacheDir: cache, HTTPClient: fixture.server.Client(), RegistryScheme: "http", Providers: []Provider{provider}})
	profile := model.TargetProfile{ID: "linux-x86_64", OS: "linux", Arch: "x86_64", RequiresSysroot: true}
	resolution, err := manager.Ensure(context.Background(), profile, EnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var lock Lock
	if err := readJSON(resolution.LockFile, &lock); err != nil {
		t.Fatal(err)
	}
	if len(lock.Layers) == 0 {
		t.Fatal("fixture lock has no layers")
	}
	algorithm, encoded, err := splitDigest(lock.Layers[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(cache, "sysroots", "blobs", algorithm, encoded)
	if err := os.WriteFile(blobPath, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove(profile.ID, true); err != nil {
		t.Fatal(err)
	}
	before := fixture.blobHits.Load()
	second, err := manager.Ensure(context.Background(), profile, EnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if second.ManifestDigest != resolution.ManifestDigest {
		t.Fatalf("manifest changed after cached blob recovery: %s != %s", second.ManifestDigest, resolution.ManifestDigest)
	}
	if fixture.blobHits.Load() <= before {
		t.Fatal("corrupt cached blob was not fetched again")
	}
	valid, err := verifyBlobFile(blobPath, descriptor{Digest: lock.Layers[0].Digest, Size: lock.Layers[0].Size})
	if err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Fatal("refetched blob still fails verification")
	}
}

func TestLookupUsesLockedProviderProvenance(t *testing.T) {
	fixture := newFakeOCI(t, false)
	defer fixture.server.Close()
	cache := t.TempDir()
	original := Provider{ID: "provider-v1", TargetID: "linux-x86_64", Image: fixture.image, OS: "linux", Architecture: "amd64"}
	profile := model.TargetProfile{ID: "linux-x86_64", OS: "linux", Arch: "x86_64", RequiresSysroot: true}
	manager := New(Options{CacheDir: cache, HTTPClient: fixture.server.Client(), RegistryScheme: "http", Providers: []Provider{original}})
	resolution, err := manager.Ensure(context.Background(), profile, EnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}

	updated := Provider{ID: "provider-v2", TargetID: profile.ID, Image: "example.invalid/new:tag", OS: "linux", Architecture: "amd64"}
	manager = New(Options{CacheDir: cache, Providers: []Provider{updated}})
	locked, found, err := manager.Lookup(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("locked sysroot disappeared after provider catalog update")
	}
	if locked.Provider != resolution.Provider || locked.Source != resolution.Source {
		t.Fatalf("lookup rewrote locked provenance: got %+v, want provider=%s source=%s", locked, resolution.Provider, resolution.Source)
	}
}

func TestParseAuthChallengePreservesLiteralPlus(t *testing.T) {
	_, parameters, err := parseAuthChallenge(`Bearer realm="https://auth.example/token+v1",service="registry.example"`)
	if err != nil {
		t.Fatal(err)
	}
	if parameters["realm"] != "https://auth.example/token+v1" {
		t.Fatalf("quoted challenge value was query-decoded: %q", parameters["realm"])
	}
}

func TestManagerRepairsIncompleteRootfsOnlineButNotOffline(t *testing.T) {
	fixture := newFakeOCI(t, false)
	defer fixture.server.Close()
	provider := Provider{ID: "test", TargetID: "linux-x86_64", Image: fixture.image, OS: "linux", Architecture: "amd64"}
	manager := New(Options{CacheDir: t.TempDir(), HTTPClient: fixture.server.Client(), RegistryScheme: "http", Providers: []Provider{provider}})
	profile := model.TargetProfile{ID: "linux-x86_64", OS: "linux", Arch: "x86_64", RequiresSysroot: true}
	resolution, err := manager.Ensure(context.Background(), profile, EnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stdio := filepath.Join(resolution.Path, "usr", "include", "stdio.h")
	if err := os.Remove(stdio); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ensure(context.Background(), profile, EnsureOptions{Offline: true}); err == nil {
		t.Fatal("offline mode silently repaired an incomplete rootfs")
	}
	blobHits := fixture.blobHits.Load()
	repaired, err := manager.Ensure(context.Background(), profile, EnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repaired.Path, "usr", "include", "stdio.h")); err != nil {
		t.Fatalf("online self-repair did not restore rootfs: %v", err)
	}
	if fixture.blobHits.Load() != blobHits+1 {
		t.Fatalf("self-repair should fetch only the image config; blob hits changed from %d to %d", blobHits, fixture.blobHits.Load())
	}
}
