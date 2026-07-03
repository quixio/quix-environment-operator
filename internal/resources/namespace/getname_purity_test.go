package namespace

import (
	"testing"

	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
)

// GetNamespaceName must be a pure getter: it derives the name without mutating env.Status,
// so a deletion retry cannot repeatedly write an unpersisted ResourceName that diverges from etcd.
func TestGetNamespaceNameIsPure(t *testing.T) {
	m := &DefaultManager{config: &config.OperatorConfig{NamespaceSuffix: "-qdep"}}
	env := &v1.Environment{Spec: v1.EnvironmentSpec{Id: "abc"}}

	if got := m.GetNamespaceName(env); got != "abc-qdep" {
		t.Fatalf("default name = %q, want %q", got, "abc-qdep")
	}
	if env.Status.NamespaceStatus != nil {
		t.Fatalf("GetNamespaceName mutated env.Status.NamespaceStatus to %+v", env.Status.NamespaceStatus)
	}

	// A persisted ResourceName must still be honored unchanged.
	env.Status.NamespaceStatus = &v1.ResourceStatus{ResourceName: "stored-ns"}
	if got := m.GetNamespaceName(env); got != "stored-ns" {
		t.Fatalf("stored name = %q, want %q", got, "stored-ns")
	}
}
