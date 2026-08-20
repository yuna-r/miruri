package analyze

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectDetectsGameSurfaceCapabilities(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "CMakeLists.txt"), "cmake_minimum_required(VERSION 3.16)\nproject(sample C)\n")
	mustWrite(t, filepath.Join(root, "renderer.c"), `
#include <immintrin.h>
#include <vulkan/vulkan.h>
#include <SDL3/SDL.h>
void *p;
void render(void) { vkCreateInstance(0, 0, 0); SDL_CreateWindow("x", 1, 1, 0); }
`)
	mustWrite(t, filepath.Join(root, "shader.hlsl"), "float4 main() : SV_Target { return 1; }\n")
	mustWrite(t, filepath.Join(root, "vendor.dll"), "not-a-real-binary")

	report, err := Project(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"cpu.x86.intrinsics":     false,
		"graphics.vulkan":        false,
		"gui.sdl":                false,
		"shader.hlsl":            false,
		"dependency.binary-only": false,
	}
	for _, requirement := range report.Requirements {
		if _, ok := want[requirement.ID]; ok {
			want[requirement.ID] = true
		}
	}
	for capability, found := range want {
		if !found {
			t.Errorf("expected capability %s", capability)
		}
	}
	if len(report.Graph.Nodes) == 0 || len(report.Graph.Edges) == 0 {
		t.Fatal("expected a populated project graph")
	}
}

func mustWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
