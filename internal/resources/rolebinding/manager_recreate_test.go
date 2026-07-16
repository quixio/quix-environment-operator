package rolebinding

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
	"github.com/quix-analytics/quix-environment-operator/internal/resources/namespace"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type noDeleteClient struct {
	client.Client
}

func (c noDeleteClient) Delete(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
	return nil
}

type currentOnDeleteClient struct {
	client.Client
	roleRef rbacv1.RoleRef
}

func (c currentOnDeleteClient) Delete(ctx context.Context, obj client.Object, _ ...client.DeleteOption) error {
	rb := &rbacv1.RoleBinding{}
	key := client.ObjectKeyFromObject(obj)
	if err := c.Client.Get(ctx, key, rb); err != nil {
		return err
	}
	if err := c.Client.Delete(ctx, rb); err != nil {
		return err
	}
	rb.ResourceVersion = ""
	rb.UID = ""
	rb.RoleRef = c.roleRef
	return c.Client.Create(ctx, rb)
}

func TestReconcileReturnsPendingWhenRecreateAlreadyExistsWithStaleRoleRef(t *testing.T) {
	sch := runtime.NewScheme()
	if err := rbacv1.AddToScheme(sch); err != nil {
		t.Fatalf("add rbac scheme: %v", err)
	}
	if err := corev1.AddToScheme(sch); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	cfg := &config.OperatorConfig{
		NamespaceSuffix:         "-qdep",
		ServiceAccountName:      "operator",
		ServiceAccountNamespace: "default",
		ClusterRoleName:         "new-role",
	}
	env := &v1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "abc-resource"}, Spec: v1.EnvironmentSpec{Id: "abc"}}
	oldRoleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "abc-quix-crb", Namespace: "abc-qdep"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "old-role",
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      cfg.ServiceAccountName,
			Namespace: cfg.ServiceAccountNamespace,
		}},
	}
	clusterRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: cfg.ClusterRoleName}}

	baseClient := fake.NewClientBuilder().
		WithScheme(sch).
		WithObjects(oldRoleBinding, clusterRole).
		Build()
	c := noDeleteClient{Client: baseClient}
	nsMgr := namespace.NewManager(c, record.NewFakeRecorder(1), cfg)
	m := NewManager(c, cfg, logr.Discard(), nsMgr)

	reconciled, err := m.Reconcile(context.Background(), env)
	if !errors.Is(err, ErrRoleBindingRecreatePending) {
		t.Fatalf("expected ErrRoleBindingRecreatePending, got roleBinding=%+v err=%v", reconciled, err)
	}
}

func TestReconcileToleratesAlreadyExistsWhenRecreatedRoleBindingIsCurrent(t *testing.T) {
	sch := runtime.NewScheme()
	if err := rbacv1.AddToScheme(sch); err != nil {
		t.Fatalf("add rbac scheme: %v", err)
	}
	if err := corev1.AddToScheme(sch); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	cfg := &config.OperatorConfig{
		NamespaceSuffix:         "-qdep",
		ServiceAccountName:      "operator",
		ServiceAccountNamespace: "default",
		ClusterRoleName:         "new-role",
	}
	env := &v1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "abc-resource"}, Spec: v1.EnvironmentSpec{Id: "abc"}}
	oldRoleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "abc-quix-crb", Namespace: "abc-qdep"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "old-role",
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      cfg.ServiceAccountName,
			Namespace: cfg.ServiceAccountNamespace,
		}},
	}
	clusterRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: cfg.ClusterRoleName}}

	baseClient := fake.NewClientBuilder().
		WithScheme(sch).
		WithObjects(oldRoleBinding, clusterRole).
		Build()
	c := currentOnDeleteClient{
		Client: baseClient,
		roleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     cfg.ClusterRoleName,
		},
	}
	nsMgr := namespace.NewManager(c, record.NewFakeRecorder(1), cfg)
	m := NewManager(c, cfg, logr.Discard(), nsMgr)

	reconciled, err := m.Reconcile(context.Background(), env)
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if reconciled.Name != oldRoleBinding.Name {
		t.Fatalf("expected fetched existing RoleBinding %q, got %q", oldRoleBinding.Name, reconciled.Name)
	}
	if reconciled.RoleRef.Name != cfg.ClusterRoleName {
		t.Fatalf("expected current RoleRef %q, got %q", cfg.ClusterRoleName, reconciled.RoleRef.Name)
	}
}
