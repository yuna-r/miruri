package sysroot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yuna-r/miruri/internal/model"
)

const (
	lockSchema = "miruri.sysroot-lock.v1"
	refSchema  = "miruri.sysroot-ref.v1"
)

type Options struct {
	CacheDir       string
	HTTPClient     *http.Client
	RegistryScheme string
	Progress       io.Writer
	Providers      []Provider
}

type EnsureOptions struct {
	Offline bool
	Refresh bool
}

type Resolution struct {
	Mode           string    `json:"mode"`
	TargetID       string    `json:"target_id"`
	Path           string    `json:"path,omitempty"`
	Provider       string    `json:"provider,omitempty"`
	Source         string    `json:"source,omitempty"`
	ManifestDigest string    `json:"manifest_digest,omitempty"`
	Platform       string    `json:"platform,omitempty"`
	LockFile       string    `json:"lock_file,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
}

func (resolution Resolution) Provenance() model.SysrootProvenance {
	return model.SysrootProvenance{
		Mode:           resolution.Mode,
		TargetID:       resolution.TargetID,
		Path:           resolution.Path,
		Provider:       resolution.Provider,
		Source:         resolution.Source,
		ManifestDigest: resolution.ManifestDigest,
		Platform:       resolution.Platform,
		LockFile:       resolution.LockFile,
	}
}

type LayerLock struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

type Lock struct {
	SchemaVersion  string      `json:"schema_version"`
	CreatedAt      time.Time   `json:"created_at"`
	TargetID       string      `json:"target_id"`
	Provider       string      `json:"provider"`
	Source         string      `json:"source"`
	ManifestDigest string      `json:"manifest_digest"`
	ConfigDigest   string      `json:"config_digest,omitempty"`
	Platform       string      `json:"platform"`
	Layers         []LayerLock `json:"layers"`
}

type targetRef struct {
	SchemaVersion  string    `json:"schema_version"`
	UpdatedAt      time.Time `json:"updated_at"`
	TargetID       string    `json:"target_id"`
	Provider       string    `json:"provider"`
	Source         string    `json:"source"`
	ManifestDigest string    `json:"manifest_digest"`
	Platform       string    `json:"platform"`
}

type Manager struct {
	root           string
	client         *http.Client
	registryScheme string
	progress       io.Writer
	providers      []Provider
}

func New(options Options) *Manager {
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	scheme := strings.TrimSpace(options.RegistryScheme)
	if scheme == "" {
		scheme = "https"
	}
	providers := append([]Provider(nil), options.Providers...)
	if len(providers) == 0 {
		providers = BuiltinProviders()
	}
	return &Manager{
		root:           DefaultCacheDir(options.CacheDir),
		client:         client,
		registryScheme: scheme,
		progress:       options.Progress,
		providers:      providers,
	}
}

func DefaultCacheDir(explicit string) string {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		if absolute, err := filepath.Abs(explicit); err == nil {
			return absolute
		}
		return explicit
	}
	if configured := strings.TrimSpace(os.Getenv("MIRURI_CACHE_DIR")); configured != "" {
		if absolute, err := filepath.Abs(configured); err == nil {
			return absolute
		}
		return configured
	}
	cacheRoot, err := os.UserCacheDir()
	if err != nil || cacheRoot == "" {
		cacheRoot = os.TempDir()
	}
	return filepath.Join(cacheRoot, "miruri")
}

func (manager *Manager) CacheDir() string {
	return manager.root
}

func (manager *Manager) Provider(profile model.TargetProfile) (Provider, bool) {
	return providerForID(profile.ID, manager.providers)
}

func (manager *Manager) Lookup(profile model.TargetProfile) (Resolution, bool, error) {
	ref, err := manager.readRef(profile.ID)
	if errors.Is(err, os.ErrNotExist) {
		return Resolution{}, false, nil
	}
	if err != nil {
		return Resolution{}, false, err
	}
	resolution, err := manager.resolutionFromRef(ref)
	if errors.Is(err, os.ErrNotExist) {
		return Resolution{}, false, nil
	}
	if err != nil {
		return Resolution{}, false, err
	}
	return resolution, true, nil
}

func (manager *Manager) Ensure(ctx context.Context, profile model.TargetProfile, options EnsureOptions) (Resolution, error) {
	provider, ok := manager.Provider(profile)
	if !ok {
		return Resolution{}, fmt.Errorf("target %s has no trusted automatic sysroot provider; pass --sysroot or set %s", profile.ID, EnvName(profile.ID))
	}
	if !options.Refresh {
		if resolution, found, err := manager.Lookup(profile); err != nil {
			if options.Offline {
				return Resolution{}, err
			}
			manager.logf("Miruri sysroot: ignoring invalid cached reference for %s: %v\n", profile.ID, err)
			if removeErr := manager.discardRef(profile.ID); removeErr != nil {
				return Resolution{}, removeErr
			}
		} else if found {
			manager.logf("Miruri sysroot: using locked %s (%s)\n", profile.ID, resolution.ManifestDigest)
			return resolution, nil
		}
	}
	if options.Offline {
		return Resolution{}, fmt.Errorf("automatic sysroot for %s is not available in the local cache and --offline forbids registry access", profile.ID)
	}

	if err := os.MkdirAll(filepath.Join(manager.root, "sysroots", "locks"), 0o755); err != nil {
		return Resolution{}, err
	}
	release, err := acquireFileLock(ctx, manager.targetLockPath(profile.ID))
	if err != nil {
		return Resolution{}, err
	}
	defer release()

	if !options.Refresh {
		if resolution, found, err := manager.Lookup(profile); err != nil {
			manager.logf("Miruri sysroot: discarding invalid cached reference for %s: %v\n", profile.ID, err)
			if removeErr := manager.discardRef(profile.ID); removeErr != nil {
				return Resolution{}, removeErr
			}
		} else if found {
			return resolution, nil
		}
	}

	manager.logf("Miruri sysroot: resolving %s for %s\n", provider.Image, PlatformString(provider))
	registry := newRegistryClient(manager.client, manager.registryScheme)
	image, err := registry.Resolve(ctx, provider.Image, provider.OS, provider.Architecture, provider.Variant)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve automatic sysroot image %s: %w", provider.Image, err)
	}
	storeDir, err := manager.storeDir(image.ManifestDigest)
	if err != nil {
		return Resolution{}, err
	}
	releaseDigest, err := acquireFileLock(ctx, manager.digestLockPath(image.ManifestDigest))
	if err != nil {
		return Resolution{}, err
	}
	defer releaseDigest()
	if resolution, found, err := manager.lookupStore(profile.ID, provider, image.ManifestDigest); err != nil {
		manager.logf("Miruri sysroot: discarding invalid content store %s: %v\n", shortDigest(image.ManifestDigest), err)
		if removeErr := os.RemoveAll(storeDir); removeErr != nil {
			return Resolution{}, fmt.Errorf("remove invalid sysroot store: %w", removeErr)
		}
	} else if found {
		if err := manager.writeRef(targetRefFromResolution(resolution)); err != nil {
			return Resolution{}, err
		}
		return resolution, nil
	}

	if err := os.MkdirAll(filepath.Dir(storeDir), 0o755); err != nil {
		return Resolution{}, err
	}
	partialDir, err := os.MkdirTemp(filepath.Dir(storeDir), filepath.Base(storeDir)+".partial-")
	if err != nil {
		return Resolution{}, err
	}
	rootfs := filepath.Join(partialDir, "rootfs")
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		return Resolution{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(partialDir)
		}
	}()

	layers := make([]LayerLock, 0, len(image.Layers))
	for index, layer := range image.Layers {
		manager.logf("Miruri sysroot: layer %d/%d %s\n", index+1, len(image.Layers), shortDigest(layer.Digest))
		blobPath, err := manager.fetchBlob(ctx, registry, image.Reference, layer)
		if err != nil {
			return Resolution{}, err
		}
		if err := applyLayer(rootfs, blobPath, layer.MediaType); err != nil {
			return Resolution{}, fmt.Errorf("extract layer %s: %w", layer.Digest, err)
		}
		layers = append(layers, LayerLock{Digest: layer.Digest, Size: layer.Size, MediaType: layer.MediaType})
	}
	if err := validateManagedRootfs(rootfs); err != nil {
		return Resolution{}, fmt.Errorf("downloaded sysroot failed validation: %w", err)
	}

	lock := Lock{
		SchemaVersion:  lockSchema,
		CreatedAt:      time.Now().UTC(),
		TargetID:       profile.ID,
		Provider:       provider.ID,
		Source:         provider.Image,
		ManifestDigest: image.ManifestDigest,
		ConfigDigest:   image.Config.Digest,
		Platform:       PlatformString(provider),
		Layers:         layers,
	}
	if err := writeJSONAtomic(filepath.Join(partialDir, "sysroot.lock.json"), lock); err != nil {
		return Resolution{}, err
	}
	if err := os.WriteFile(filepath.Join(partialDir, ".complete"), []byte(image.ManifestDigest+"\n"), 0o644); err != nil {
		return Resolution{}, err
	}
	if err := os.Rename(partialDir, storeDir); err != nil {
		return Resolution{}, fmt.Errorf("commit sysroot cache: %w", err)
	}
	cleanup = false

	resolution := Resolution{
		Mode:           "managed",
		TargetID:       profile.ID,
		Path:           filepath.Join(storeDir, "rootfs"),
		Provider:       provider.ID,
		Source:         provider.Image,
		ManifestDigest: image.ManifestDigest,
		Platform:       PlatformString(provider),
		LockFile:       filepath.Join(storeDir, "sysroot.lock.json"),
		CreatedAt:      lock.CreatedAt,
	}
	if err := manager.writeRef(targetRefFromResolution(resolution)); err != nil {
		return Resolution{}, err
	}
	manager.logf("Miruri sysroot: ready %s\n", resolution.Path)
	return resolution, nil
}

func (manager *Manager) List() ([]Resolution, error) {
	refsDir := filepath.Join(manager.root, "sysroots", "refs")
	entries, err := os.ReadDir(refsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var resolutions []Resolution
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var ref targetRef
		if err := readJSON(filepath.Join(refsDir, entry.Name()), &ref); err != nil {
			continue
		}
		resolution, err := manager.resolutionFromRef(ref)
		if err == nil {
			resolutions = append(resolutions, resolution)
		}
	}
	sort.Slice(resolutions, func(i, j int) bool { return resolutions[i].TargetID < resolutions[j].TargetID })
	return resolutions, nil
}

func (manager *Manager) Remove(targetID string, purge bool) error {
	ref, err := manager.readRef(targetID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(manager.refPath(targetID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !purge || ref.ManifestDigest == "" {
		return nil
	}
	used, err := manager.digestReferenced(ref.ManifestDigest)
	if err != nil {
		return err
	}
	if used {
		return nil
	}
	storeDir, err := manager.storeDir(ref.ManifestDigest)
	if err != nil {
		return err
	}
	return os.RemoveAll(storeDir)
}

func (manager *Manager) lookupStore(targetID string, provider Provider, digest string) (Resolution, bool, error) {
	storeDir, err := manager.storeDir(digest)
	if err != nil {
		return Resolution{}, false, err
	}
	if _, err := os.Stat(filepath.Join(storeDir, ".complete")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Resolution{}, false, nil
		}
		return Resolution{}, false, err
	}
	var lock Lock
	lockPath := filepath.Join(storeDir, "sysroot.lock.json")
	if err := readJSON(lockPath, &lock); err != nil {
		return Resolution{}, false, err
	}
	if lock.SchemaVersion != lockSchema || lock.ManifestDigest != digest {
		return Resolution{}, false, fmt.Errorf("invalid sysroot lock in %s", lockPath)
	}
	rootfs := filepath.Join(storeDir, "rootfs")
	if err := validateManagedRootfs(rootfs); err != nil {
		return Resolution{}, false, fmt.Errorf("cached sysroot %s is incomplete: %w", digest, err)
	}
	return Resolution{
		Mode:           "managed",
		TargetID:       targetID,
		Path:           rootfs,
		Provider:       provider.ID,
		Source:         provider.Image,
		ManifestDigest: digest,
		Platform:       PlatformString(provider),
		LockFile:       lockPath,
		CreatedAt:      lock.CreatedAt,
	}, true, nil
}

func (manager *Manager) resolutionFromRef(ref targetRef) (Resolution, error) {
	provider := Provider{ID: ref.Provider, TargetID: ref.TargetID, Image: ref.Source}
	parts := strings.Split(ref.Platform, "/")
	if len(parts) > 0 {
		provider.OS = parts[0]
	}
	if len(parts) > 1 {
		provider.Architecture = parts[1]
	}
	if len(parts) > 2 {
		provider.Variant = parts[2]
	}
	if provider.ID == "" || provider.Image == "" || provider.OS == "" || provider.Architecture == "" {
		if current, ok := providerForID(ref.TargetID, manager.providers); ok {
			if provider.ID == "" {
				provider.ID = current.ID
			}
			if provider.Image == "" {
				provider.Image = current.Image
			}
			if provider.OS == "" {
				provider.OS = current.OS
			}
			if provider.Architecture == "" {
				provider.Architecture = current.Architecture
			}
			if provider.Variant == "" {
				provider.Variant = current.Variant
			}
		}
	}
	resolution, found, err := manager.lookupStore(ref.TargetID, provider, ref.ManifestDigest)
	if err != nil {
		return Resolution{}, err
	}
	if !found {
		return Resolution{}, os.ErrNotExist
	}
	return resolution, nil
}

func (manager *Manager) fetchBlob(ctx context.Context, registry *registryClient, reference imageReference, descriptor descriptor) (string, error) {
	algorithm, encoded, err := splitDigest(descriptor.Digest)
	if err != nil {
		return "", err
	}
	if algorithm != "sha256" {
		return "", fmt.Errorf("unsupported OCI digest algorithm %s", algorithm)
	}
	blobPath := filepath.Join(manager.root, "sysroots", "blobs", algorithm, encoded)
	if info, err := os.Stat(blobPath); err == nil && info.Mode().IsRegular() {
		valid, verifyErr := verifyBlobFile(blobPath, descriptor)
		if verifyErr != nil {
			return "", verifyErr
		}
		if valid {
			return blobPath, nil
		}
		manager.logf("Miruri sysroot: cached blob %s failed verification; downloading it again\n", shortDigest(descriptor.Digest))
		if err := os.Remove(blobPath); err != nil {
			return "", fmt.Errorf("remove invalid cached OCI blob %s: %w", descriptor.Digest, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o755); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(filepath.Dir(blobPath), ".blob-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()

	body, err := registry.Blob(ctx, reference, descriptor.Digest)
	if err != nil {
		return "", err
	}
	defer body.Close()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), body)
	closeErr := temporary.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != encoded {
		return "", fmt.Errorf("OCI blob digest mismatch: expected %s, got sha256:%s", descriptor.Digest, actual)
	}
	if descriptor.Size > 0 && written != descriptor.Size {
		return "", fmt.Errorf("OCI blob size mismatch for %s: expected %d, got %d", descriptor.Digest, descriptor.Size, written)
	}
	if err := os.Rename(temporaryPath, blobPath); err != nil {
		if _, statErr := os.Stat(blobPath); statErr != nil {
			return "", err
		}
	}
	return blobPath, nil
}

func verifyBlobFile(path string, descriptor descriptor) (bool, error) {
	algorithm, expected, err := splitDigest(descriptor.Digest)
	if err != nil {
		return false, err
	}
	if algorithm != "sha256" {
		return false, fmt.Errorf("unsupported OCI digest algorithm %s", algorithm)
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, file)
	if err != nil {
		return false, err
	}
	if descriptor.Size > 0 && written != descriptor.Size {
		return false, nil
	}
	return hex.EncodeToString(hash.Sum(nil)) == expected, nil
}

func (manager *Manager) digestReferenced(digest string) (bool, error) {
	refsDir := filepath.Join(manager.root, "sysroots", "refs")
	entries, err := os.ReadDir(refsDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var ref targetRef
		if err := readJSON(filepath.Join(refsDir, entry.Name()), &ref); err == nil && ref.ManifestDigest == digest {
			return true, nil
		}
	}
	return false, nil
}

func (manager *Manager) readRef(targetID string) (targetRef, error) {
	var ref targetRef
	if err := readJSON(manager.refPath(targetID), &ref); err != nil {
		return targetRef{}, err
	}
	if ref.SchemaVersion != refSchema || ref.TargetID != targetID {
		return targetRef{}, fmt.Errorf("invalid sysroot reference for %s", targetID)
	}
	return ref, nil
}

func (manager *Manager) writeRef(ref targetRef) error {
	return writeJSONAtomic(manager.refPath(ref.TargetID), ref)
}

func (manager *Manager) discardRef(targetID string) error {
	if err := os.Remove(manager.refPath(targetID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove invalid sysroot reference for %s: %w", targetID, err)
	}
	return nil
}

func targetRefFromResolution(resolution Resolution) targetRef {
	return targetRef{
		SchemaVersion:  refSchema,
		UpdatedAt:      time.Now().UTC(),
		TargetID:       resolution.TargetID,
		Provider:       resolution.Provider,
		Source:         resolution.Source,
		ManifestDigest: resolution.ManifestDigest,
		Platform:       resolution.Platform,
	}
}

func (manager *Manager) refPath(targetID string) string {
	return filepath.Join(manager.root, "sysroots", "refs", safeName(targetID)+".json")
}

func (manager *Manager) targetLockPath(targetID string) string {
	return filepath.Join(manager.root, "sysroots", "locks", safeName(targetID)+".lock")
}

func (manager *Manager) digestLockPath(digest string) string {
	_, encoded, err := splitDigest(digest)
	if err != nil {
		encoded = safeName(digest)
	}
	return filepath.Join(manager.root, "sysroots", "locks", "digests", encoded+".lock")
}

func (manager *Manager) storeDir(digest string) (string, error) {
	algorithm, encoded, err := splitDigest(digest)
	if err != nil {
		return "", err
	}
	return filepath.Join(manager.root, "sysroots", "store", algorithm, encoded), nil
}

func (manager *Manager) logf(format string, args ...any) {
	if manager.progress != nil {
		_, _ = fmt.Fprintf(manager.progress, format, args...)
	}
}

func EnvName(targetID string) string {
	replacer := strings.NewReplacer("-", "_", ".", "_", "/", "_")
	return "MIRURI_SYSROOT_" + strings.ToUpper(replacer.Replace(targetID))
}

func safeName(value string) string {
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '-', char == '_', char == '.':
			builder.WriteRune(char)
		default:
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "target"
	}
	return builder.String()
}

func splitDigest(digest string) (string, string, error) {
	algorithm, encoded, ok := strings.Cut(digest, ":")
	if !ok || algorithm == "" || encoded == "" {
		return "", "", fmt.Errorf("invalid OCI digest %q", digest)
	}
	if algorithm != "sha256" || len(encoded) != 64 {
		return "", "", fmt.Errorf("unsupported or malformed OCI digest %q", digest)
	}
	if _, err := hex.DecodeString(encoded); err != nil {
		return "", "", fmt.Errorf("invalid OCI digest %q", digest)
	}
	return algorithm, strings.ToLower(encoded), nil
}

func shortDigest(digest string) string {
	if len(digest) <= 20 {
		return digest
	}
	return digest[:20]
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".json-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func acquireFileLock(ctx context.Context, path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "pid=%d\ncreated=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
			_ = file.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > 2*time.Hour {
			_ = os.Remove(path)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}
