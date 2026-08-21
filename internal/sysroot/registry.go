package sysroot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

type descriptor struct {
	MediaType string    `json:"mediaType"`
	Digest    string    `json:"digest"`
	Size      int64     `json:"size,omitempty"`
	Platform  *platform `json:"platform,omitempty"`
}

type platform struct {
	Architecture string   `json:"architecture"`
	OS           string   `json:"os"`
	Variant      string   `json:"variant,omitempty"`
	OSVersion    string   `json:"os.version,omitempty"`
	Features     []string `json:"features,omitempty"`
}

type manifestIndex struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Manifests     []descriptor `json:"manifests"`
}

type imageManifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
}

type imageConfig struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
}

type resolvedImage struct {
	Reference      imageReference
	ManifestDigest string
	Config         descriptor
	Layers         []descriptor
}

type imageReference struct {
	Registry    string
	APIRegistry string
	Repository  string
	Reference   string
}

func (reference imageReference) String() string {
	return reference.Registry + "/" + reference.Repository + ":" + reference.Reference
}

type registryClient struct {
	client *http.Client
	scheme string
	mu     sync.Mutex
	tokens map[string]string
}

func newRegistryClient(client *http.Client, scheme string) *registryClient {
	return &registryClient{client: client, scheme: scheme, tokens: make(map[string]string)}
}

func (registry *registryClient) Resolve(ctx context.Context, image, osName, architecture, variant string) (resolvedImage, error) {
	reference, err := parseImageReference(image)
	if err != nil {
		return resolvedImage{}, err
	}
	body, digest, mediaType, err := registry.manifest(ctx, reference, reference.Reference)
	if err != nil {
		return resolvedImage{}, err
	}

	if isIndexMediaType(mediaType, body) {
		var index manifestIndex
		if err := json.Unmarshal(body, &index); err != nil {
			return resolvedImage{}, fmt.Errorf("parse OCI image index: %w", err)
		}
		if index.SchemaVersion != 2 {
			return resolvedImage{}, fmt.Errorf("unsupported OCI image index schema version %d", index.SchemaVersion)
		}
		selected, err := selectPlatform(index.Manifests, osName, architecture, variant)
		if err != nil {
			return resolvedImage{}, err
		}
		body, digest, mediaType, err = registry.manifest(ctx, reference, selected.Digest)
		if err != nil {
			return resolvedImage{}, err
		}
		if digest != selected.Digest {
			return resolvedImage{}, fmt.Errorf("selected manifest digest mismatch: index declared %s, registry returned %s", selected.Digest, digest)
		}
		if selected.Size > 0 && int64(len(body)) != selected.Size {
			return resolvedImage{}, fmt.Errorf("selected manifest size mismatch: index declared %d, registry returned %d", selected.Size, len(body))
		}
	}
	if !isManifestMediaType(mediaType, body) {
		return resolvedImage{}, fmt.Errorf("registry returned unsupported manifest media type %q", mediaType)
	}
	var manifest imageManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return resolvedImage{}, fmt.Errorf("parse OCI image manifest: %w", err)
	}
	if manifest.SchemaVersion != 2 {
		return resolvedImage{}, fmt.Errorf("unsupported OCI image manifest schema version %d", manifest.SchemaVersion)
	}
	if len(manifest.Layers) == 0 {
		return resolvedImage{}, fmt.Errorf("OCI image manifest %s has no layers", digest)
	}
	if manifest.Config.Digest == "" {
		return resolvedImage{}, fmt.Errorf("OCI image manifest %s has no config descriptor", digest)
	}
	if err := registry.verifyImageConfig(ctx, reference, manifest.Config, osName, architecture, variant); err != nil {
		return resolvedImage{}, err
	}
	return resolvedImage{Reference: reference, ManifestDigest: digest, Config: manifest.Config, Layers: manifest.Layers}, nil
}

func (registry *registryClient) Blob(ctx context.Context, reference imageReference, digest string) (io.ReadCloser, error) {
	path := registryURL(registry.scheme, reference.APIRegistry, reference.Repository, "blobs", digest)
	response, err := registry.do(ctx, reference, http.MethodGet, path, "")
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		return nil, responseError("fetch OCI blob "+digest, response)
	}
	return response.Body, nil
}

func (registry *registryClient) manifest(ctx context.Context, reference imageReference, requested string) ([]byte, string, string, error) {
	path := registryURL(registry.scheme, reference.APIRegistry, reference.Repository, "manifests", requested)
	response, err := registry.do(ctx, reference, http.MethodGet, path, manifestAccept)
	if err != nil {
		return nil, "", "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", "", responseError("fetch OCI manifest "+requested, response)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, "", "", err
	}
	sum := sha256.Sum256(body)
	computed := "sha256:" + hex.EncodeToString(sum[:])
	if strings.HasPrefix(requested, "sha256:") && requested != computed {
		return nil, "", "", fmt.Errorf("OCI manifest digest mismatch: requested %s, received %s", requested, computed)
	}
	if declared := strings.TrimSpace(response.Header.Get("Docker-Content-Digest")); declared != "" && declared != computed {
		return nil, "", "", fmt.Errorf("OCI manifest digest mismatch: registry declared %s, body is %s", declared, computed)
	}
	mediaType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if mediaType == "" {
		var envelope struct {
			MediaType string `json:"mediaType"`
		}
		_ = json.Unmarshal(body, &envelope)
		mediaType = envelope.MediaType
	}
	return body, computed, mediaType, nil
}

func (registry *registryClient) verifyImageConfig(ctx context.Context, reference imageReference, descriptor descriptor, osName, architecture, variant string) error {
	body, err := registry.Blob(ctx, reference, descriptor.Digest)
	if err != nil {
		return fmt.Errorf("fetch OCI image config: %w", err)
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 8<<20))
	if err != nil {
		return err
	}
	algorithm, expected, err := splitDigest(descriptor.Digest)
	if err != nil {
		return err
	}
	if algorithm != "sha256" {
		return fmt.Errorf("unsupported image config digest %s", descriptor.Digest)
	}
	sum := sha256.Sum256(data)
	if actual := hex.EncodeToString(sum[:]); actual != expected {
		return fmt.Errorf("OCI image config digest mismatch: expected %s, got sha256:%s", descriptor.Digest, actual)
	}
	if descriptor.Size > 0 && int64(len(data)) != descriptor.Size {
		return fmt.Errorf("OCI image config size mismatch: expected %d, got %d", descriptor.Size, len(data))
	}
	var config imageConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse OCI image config: %w", err)
	}
	if config.OS != osName {
		return fmt.Errorf("OCI image config OS mismatch: expected %s, got %s", osName, config.OS)
	}
	if config.Architecture != architecture {
		return fmt.Errorf("OCI image config architecture mismatch: expected %s, got %s", architecture, config.Architecture)
	}
	if variant != "" && config.Variant != variant {
		return fmt.Errorf("OCI image config variant mismatch: expected %s, got %s", variant, config.Variant)
	}
	return nil
}

func (registry *registryClient) do(ctx context.Context, reference imageReference, method, endpoint, accept string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "miruri-sysroot/1")
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	registry.mu.Lock()
	token := registry.tokens[reference.APIRegistry+"/"+reference.Repository]
	registry.mu.Unlock()
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := registry.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusUnauthorized {
		return response, nil
	}
	challenge := response.Header.Get("WWW-Authenticate")
	if strings.TrimSpace(challenge) == "" {
		return response, nil
	}
	_ = response.Body.Close()
	token, err = registry.fetchToken(ctx, challenge, reference.Repository)
	if err != nil {
		return nil, err
	}
	registry.mu.Lock()
	registry.tokens[reference.APIRegistry+"/"+reference.Repository] = token
	registry.mu.Unlock()

	retry, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, err
	}
	retry.Header.Set("User-Agent", "miruri-sysroot/1")
	if accept != "" {
		retry.Header.Set("Accept", accept)
	}
	retry.Header.Set("Authorization", "Bearer "+token)
	return registry.client.Do(retry)
}

func (registry *registryClient) fetchToken(ctx context.Context, challenge, repository string) (string, error) {
	scheme, params, err := parseAuthChallenge(challenge)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(scheme, "Bearer") {
		return "", fmt.Errorf("unsupported OCI registry authentication scheme %q", scheme)
	}
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("OCI registry Bearer challenge has no realm")
	}
	tokenURL, err := url.Parse(realm)
	if err != nil {
		return "", err
	}
	query := tokenURL.Query()
	if service := params["service"]; service != "" {
		query.Set("service", service)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + repository + ":pull"
	}
	query.Set("scope", scope)
	tokenURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "miruri-sysroot/1")
	response, err := registry.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", responseError("request OCI registry token", response)
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload); err != nil {
		return "", fmt.Errorf("parse OCI registry token response: %w", err)
	}
	if payload.Token == "" {
		payload.Token = payload.AccessToken
	}
	if payload.Token == "" {
		return "", fmt.Errorf("OCI registry token response contained no token")
	}
	return payload.Token, nil
}

func parseImageReference(value string) (imageReference, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "://") {
		return imageReference{}, fmt.Errorf("invalid OCI image reference %q", value)
	}
	var reference string
	if at := strings.LastIndex(value, "@"); at >= 0 {
		reference = value[at+1:]
		value = value[:at]
	} else {
		lastSlash := strings.LastIndex(value, "/")
		lastColon := strings.LastIndex(value, ":")
		if lastColon > lastSlash {
			reference = value[lastColon+1:]
			value = value[:lastColon]
		} else {
			reference = "latest"
		}
	}
	parts := strings.Split(value, "/")
	registryName := "docker.io"
	repositoryParts := parts
	if len(parts) > 1 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") || parts[0] == "localhost") {
		registryName = parts[0]
		repositoryParts = parts[1:]
	}
	if len(repositoryParts) == 1 && registryName == "docker.io" {
		repositoryParts = append([]string{"library"}, repositoryParts...)
	}
	repository := strings.Join(repositoryParts, "/")
	if repository == "" || reference == "" {
		return imageReference{}, fmt.Errorf("invalid OCI image reference")
	}
	apiRegistry := registryName
	if registryName == "docker.io" || registryName == "index.docker.io" {
		registryName = "docker.io"
		apiRegistry = "registry-1.docker.io"
	}
	return imageReference{Registry: registryName, APIRegistry: apiRegistry, Repository: repository, Reference: reference}, nil
}

func selectPlatform(descriptors []descriptor, osName, architecture, variant string) (descriptor, error) {
	var candidates []descriptor
	for _, candidate := range descriptors {
		if candidate.Platform == nil || candidate.Platform.OS != osName || candidate.Platform.Architecture != architecture {
			continue
		}
		if variant != "" && candidate.Platform.Variant != variant {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return descriptor{}, fmt.Errorf("OCI image has no manifest for %s/%s%s", osName, architecture, variantSuffix(variant))
	}
	for _, candidate := range candidates {
		if candidate.Platform.Variant == variant {
			return candidate, nil
		}
	}
	return candidates[0], nil
}

func variantSuffix(variant string) string {
	if variant == "" {
		return ""
	}
	return "/" + variant
}

func registryURL(scheme, registry, repository, kind, value string) string {
	return scheme + "://" + registry + "/v2/" + repository + "/" + kind + "/" + url.PathEscape(value)
}

func isIndexMediaType(mediaType string, body []byte) bool {
	if mediaType == "application/vnd.oci.image.index.v1+json" || mediaType == "application/vnd.docker.distribution.manifest.list.v2+json" {
		return true
	}
	var probe struct {
		Manifests json.RawMessage `json:"manifests"`
	}
	return json.Unmarshal(body, &probe) == nil && len(probe.Manifests) > 0 && !bytes.Equal(probe.Manifests, []byte("null"))
}

func isManifestMediaType(mediaType string, body []byte) bool {
	if mediaType == "application/vnd.oci.image.manifest.v1+json" || mediaType == "application/vnd.docker.distribution.manifest.v2+json" {
		return true
	}
	var probe struct {
		Layers json.RawMessage `json:"layers"`
	}
	return json.Unmarshal(body, &probe) == nil && len(probe.Layers) > 0 && !bytes.Equal(probe.Layers, []byte("null"))
}

func parseAuthChallenge(value string) (string, map[string]string, error) {
	value = strings.TrimSpace(value)
	space := strings.IndexByte(value, ' ')
	if space <= 0 {
		return "", nil, fmt.Errorf("invalid OCI registry authentication challenge %q", value)
	}
	scheme := value[:space]
	raw := strings.TrimSpace(value[space+1:])
	parts := splitQuotedComma(raw)
	params := make(map[string]string, len(parts))
	for _, part := range parts {
		key, encoded, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		encoded = strings.TrimSpace(encoded)
		if len(encoded) >= 2 && encoded[0] == '"' && encoded[len(encoded)-1] == '"' {
			if decoded, err := strconv.Unquote(encoded); err == nil {
				encoded = decoded
			} else {
				encoded = encoded[1 : len(encoded)-1]
			}
		}
		params[strings.ToLower(strings.TrimSpace(key))] = encoded
	}
	return scheme, params, nil
}

func splitQuotedComma(value string) []string {
	var result []string
	start := 0
	quoted := false
	escaped := false
	for index, char := range value {
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' && quoted {
			escaped = true
			continue
		}
		if char == '"' {
			quoted = !quoted
			continue
		}
		if char == ',' && !quoted {
			result = append(result, value[start:index])
			start = index + 1
		}
	}
	result = append(result, value[start:])
	return result
}

func responseError(action string, response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	message := strings.TrimSpace(string(body))
	if message == "" {
		return fmt.Errorf("%s: HTTP %s", action, response.Status)
	}
	return fmt.Errorf("%s: HTTP %s: %s", action, response.Status, message)
}
