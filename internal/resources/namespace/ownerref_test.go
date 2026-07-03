package namespace

import (
	"context"
	"testing"

	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A namespace created for an Environment must carry a controller owner reference back to that
// Environment so Kubernetes garbage collection can reclaim it as a fallback.
func TestCreateSetsEnvironmentOwnerReference(t *testing.T) {
	sch := runtime.NewScheme()
	if err := corev1.AddToScheme(sch); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(sch).Build()
	m := NewManager(c, record.NewFakeRecorder(5), &config.OperatorConfig{NamespaceSuffix: "-qenv"})

	env := &v1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "env-a", UID: "uid-123"},
		Spec:       v1.EnvironmentSpec{Id: "abc"},
	}

	ns, err := m.create(context.Background(), env)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ref := metav1.GetControllerOf(ns)
	if ref == nil {
		t.Fatalf("created namespace has no controller owner reference")
	}
	if ref.Kind != "Environment" || ref.Name != "env-a" || string(ref.UID) != "uid-123" {
		t.Fatalf("unexpected owner reference: %+v", ref)
	}
	if ref.APIVersion != v1.GroupVersion.String() {
		t.Fatalf("owner reference APIVersion = %q, want %q", ref.APIVersion, v1.GroupVersion.String())
	}
	if ref.BlockOwnerDeletion == nil || !*ref.BlockOwnerDeletion {
		t.Fatalf("owner reference BlockOwnerDeletion = %v, want true", ref.BlockOwnerDeletion)
	}
}

func TestApplyMetadataEnsuresEnvironmentOwnerReferenceIdempotently(t *testing.T) {
	m := &DefaultManager{}
	env := &v1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "env-a", UID: "uid-123"},
		Spec:       v1.EnvironmentSpec{Id: "abc"},
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "abc-qdep"}}

	if changed := m.ApplyMetadata(env, ns); !changed {
		t.Fatalf("ApplyMetadata should report changes when adding metadata and owner reference")
	}

	ref := metav1.GetControllerOf(ns)
	if ref == nil {
		t.Fatalf("namespace has no controller owner reference")
	}
	if ref.Kind != "Environment" || ref.Name != "env-a" || string(ref.UID) != "uid-123" {
		t.Fatalf("unexpected owner reference: %+v", ref)
	}
	if ref.BlockOwnerDeletion == nil || !*ref.BlockOwnerDeletion {
		t.Fatalf("owner reference BlockOwnerDeletion = %v, want true", ref.BlockOwnerDeletion)
	}

	if changed := m.ApplyMetadata(env, ns); changed {
		t.Fatalf("ApplyMetadata should be idempotent when metadata and owner reference already match")
	}
	if refs := ns.OwnerReferences; len(refs) != 1 {
		t.Fatalf("expected exactly one owner reference after idempotent apply, got %d: %+v", len(refs), refs)
	}
}

func TestApplyMetadataReplacesStaleEnvironmentOwnerReference(t *testing.T) {
	m := &DefaultManager{}
	env := &v1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "env-a", UID: "uid-123"},
		Spec:       v1.EnvironmentSpec{Id: "abc"},
	}
	oldController := true
	oldBlockOwnerDeletion := true
	otherController := false
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "abc-qdep",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "other-owner",
					UID:        "other-uid",
					Controller: &otherController,
				},
				{
					APIVersion:         v1.GroupVersion.String(),
					Kind:               "Environment",
					Name:               "deleted-env",
					UID:                "deleted-uid",
					Controller:         &oldController,
					BlockOwnerDeletion: &oldBlockOwnerDeletion,
				},
				{
					APIVersion:         v1.GroupVersion.String(),
					Kind:               "Environment",
					Name:               "env-a",
					UID:                "uid-123",
					Controller:         &oldController,
					BlockOwnerDeletion: &oldBlockOwnerDeletion,
				},
			},
		},
	}

	if changed := m.ApplyMetadata(env, ns); !changed {
		t.Fatalf("ApplyMetadata should report changes when replacing stale owner reference")
	}

	if refs := ns.OwnerReferences; len(refs) != 2 {
		t.Fatalf("expected one non-Environment ref plus one Environment ref after stale replacement, got %d: %+v", len(refs), refs)
	}
	ref := metav1.GetControllerOf(ns)
	if ref == nil {
		t.Fatalf("namespace has no controller owner reference")
	}
	if ref.Name != "env-a" || string(ref.UID) != "uid-123" {
		t.Fatalf("unexpected replacement owner reference: %+v", ref)
	}
	if ref.BlockOwnerDeletion == nil || !*ref.BlockOwnerDeletion {
		t.Fatalf("owner reference BlockOwnerDeletion = %v, want true", ref.BlockOwnerDeletion)
	}
}

func TestApplyMetadataSkipsOwnerReferenceWhenEnvironmentUIDIsEmpty(t *testing.T) {
	m := &DefaultManager{}
	env := &v1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "env-a"},
		Spec:       v1.EnvironmentSpec{Id: "abc"},
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "abc-qdep"}}

	m.ApplyMetadata(env, ns)

	if ref := metav1.GetControllerOf(ns); ref != nil {
		t.Fatalf("expected no owner reference for an Environment without UID, got %+v", ref)
	}
}

func TestUpdatePersistsStaleEnvironmentOwnerReferenceReplacement(t *testing.T) {
	sch := runtime.NewScheme()
	if err := corev1.AddToScheme(sch); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	oldController := true
	oldBlockOwnerDeletion := true
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "abc-qdep",
			Labels: map[string]string{
				ManagedByLabel:       OperatorName,
				LabelEnvironmentID:   "abc",
				LabelEnvironmentName: "env-a",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         v1.GroupVersion.String(),
					Kind:               "Environment",
					Name:               "deleted-env",
					UID:                "deleted-uid",
					Controller:         &oldController,
					BlockOwnerDeletion: &oldBlockOwnerDeletion,
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(sch).WithObjects(ns).Build()
	m := NewManager(c, record.NewFakeRecorder(5), &config.OperatorConfig{NamespaceSuffix: "-qdep"})
	env := &v1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "env-a", UID: "uid-123"},
		Spec:       v1.EnvironmentSpec{Id: "abc"},
	}

	if err := m.update(context.Background(), env); err != nil {
		t.Fatalf("update: %v", err)
	}

	updated := &corev1.Namespace{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "abc-qdep"}, updated); err != nil {
		t.Fatalf("get updated namespace: %v", err)
	}
	ref := metav1.GetControllerOf(updated)
	if ref == nil {
		t.Fatalf("namespace has no controller owner reference")
	}
	if ref.Name != "env-a" || string(ref.UID) != "uid-123" {
		t.Fatalf("unexpected persisted owner reference: %+v", ref)
	}
}
