package workloads

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestHasVolumeImportSourcePopulator(t *testing.T) {
	registered := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "populator.storage.k8s.io/v1beta1",
		"kind":       "VolumePopulator",
		"metadata":   map[string]any{"name": "volumeimportsource"},
		"sourceKind": map[string]any{"group": "cdi.kubevirt.io", "kind": "VolumeImportSource"},
	}}
	client := newVolumePopulatorTestClient(registered)

	got, err := hasVolumeImportSourcePopulator(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatal("expected VolumeImportSource populator to be detected")
	}
}

func TestHasVolumeImportSourcePopulatorWhenAbsent(t *testing.T) {
	client := newVolumePopulatorTestClient()

	got, err := hasVolumeImportSourcePopulator(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatal("did not expect VolumeImportSource populator to be detected")
	}
}

func newVolumePopulatorTestClient(objects ...runtime.Object) *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{volumePopulatorGVR: "VolumePopulatorList"},
		objects...,
	)
}
