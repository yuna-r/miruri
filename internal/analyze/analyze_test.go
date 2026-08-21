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

func TestProjectDetectsBuildAndPlatformPortabilityIslands(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "CMakeLists.txt"), `
cmake_minimum_required(VERSION 3.16)
project(portability C)
add_compile_options(-march=native)
try_run(RUN_RESULT COMPILE_RESULT probe.c)
`)
	mustWrite(t, filepath.Join(root, "platform.c"), `
#include <windows.h>
#include <cuda_runtime.h>
#pragma pack(push, 1)
struct Packet { char kind; int value; };
#pragma pack(pop)
void probe(void) {
  void *memory = VirtualAlloc(0, 4096, MEM_COMMIT, PAGE_READWRITE);
  QueryPerformanceCounter(0);
  cudaMalloc(&memory, 4096);
}
`)

	report, err := Project(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"build.native-cpu-flags":       false,
		"build.target-execution-probe": false,
		"compute.cuda":                 false,
		"abi.packed-layout":            false,
		"memory.win32-virtual":         false,
		"time.win32-qpc":               false,
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
}

func TestProjectExcludesExplicitGeneratedPathFromDigestAndCapabilities(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "CMakeLists.txt"), "cmake_minimum_required(VERSION 3.16)\nproject(excluded C)\n")
	mustWrite(t, filepath.Join(root, "main.c"), "int main(void) { return 0; }\n")
	baseline, err := Project(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	generated := filepath.Join(root, "custom-results")
	if err := os.MkdirAll(generated, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(generated, "generated.c"), "#include <cuda_runtime.h>\nvoid f(void) { cudaMalloc(0, 1); }\n")
	report, err := Project(root, Options{ExcludePaths: []string{generated}})
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectDigest != baseline.ProjectDigest || report.FileCount != baseline.FileCount || report.ProjectEntries != baseline.ProjectEntries {
		t.Fatalf("explicit output exclusion changed source identity: baseline=%+v report=%+v", baseline, report)
	}
	for _, requirement := range report.Requirements {
		if requirement.ID == "compute.cuda" {
			t.Fatalf("excluded generated source leaked capability evidence: %+v", requirement)
		}
	}
	for _, node := range report.Graph.Nodes {
		if node.Path == "custom-results/generated.c" {
			t.Fatalf("excluded generated source leaked into graph: %+v", node)
		}
	}
}
