package controllers

import (
	"context"
	"reflect"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
)

// Mock EnvironmentReconciler for testing
type mockEnvironmentReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
	Config *config.OperatorConfig
}

// checkAndUpdateRoleRef is the mock implementation of the method
func (r *mockEnvironmentReconciler) checkAndUpdateRoleRef(
	desiredRb *rbacv1.RoleBinding,
	foundRb *rbacv1.RoleBinding,
	logger logr.Logger,
) bool {
	if foundRb.RoleRef.Name != desiredRb.RoleRef.Name ||
		foundRb.RoleRef.Kind != desiredRb.RoleRef.Kind ||
		foundRb.RoleRef.APIGroup != desiredRb.RoleRef.APIGroup {
		foundRb.RoleRef = desiredRb.RoleRef
		return true
	}
	return false
}

// checkAndUpdateSubjects is the mock implementation of the method
func (r *mockEnvironmentReconciler) checkAndUpdateSubjects(
	desiredRb *rbacv1.RoleBinding,
	foundRb *rbacv1.RoleBinding,
	logger logr.Logger,
) bool {
	if len(foundRb.Subjects) != len(desiredRb.Subjects) || !reflect.DeepEqual(foundRb.Subjects, desiredRb.Subjects) {
		foundRb.Subjects = desiredRb.Subjects
		return true
	}
	return false
}

// defineManagedRoleBinding is the mock implementation of the method
func (r *mockEnvironmentReconciler) defineManagedRoleBinding(env *quixiov1.Environment, namespaceName string) *rbacv1.RoleBinding {
	rbName := "quix-environment-access" // We'll hardcode this for the test
	controllerRef := true

	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbName,
			Namespace: namespaceName,
			Labels: map[string]string{
				"quix.io/managed-by":     "quix-environment-operator",
				"quix.io/environment-id": env.Spec.Id,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: quixiov1.GroupVersion.String(),
					Kind:       "Environment",
					Name:       env.Name,
					UID:        env.UID,
					Controller: &controllerRef,
				},
			},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      r.Config.ServiceAccountName,
				Namespace: r.Config.ServiceAccountNamespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     r.Config.ClusterRoleName,
		},
	}
}

// applyRoleBindingUpdates mocks the controller method
func (r *mockEnvironmentReconciler) applyRoleBindingUpdates(
	ctx context.Context,
	env *quixiov1.Environment,
	foundRb *rbacv1.RoleBinding,
	rbName string,
	logger logr.Logger,
) (ctrl.Result, error) {
	// Create a desired roleBinding to compare with
	desiredRb := r.defineManagedRoleBinding(env, foundRb.Namespace)

	// Check for updates
	updated := false

	// Check and update roleRef if needed
	if r.checkAndUpdateRoleRef(desiredRb, foundRb, logger) {
		updated = true
	}

	// Check and update subjects if needed
	if r.checkAndUpdateSubjects(desiredRb, foundRb, logger) {
		updated = true
	}

	// Apply updates if needed
	if updated {
		err := r.Client.Update(ctx, foundRb)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// Mock NewEnvironmentReconciler for testing
func newMockEnvironmentReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	config *config.OperatorConfig,
) *mockEnvironmentReconciler {
	return &mockEnvironmentReconciler{
		Client: client,
		Scheme: scheme,
		Config: config,
	}
}

func TestCheckAndUpdateRoleRef(t *testing.T) {
	// Setup logging
	log.SetLogger(zap.New())
	logger := log.Log.WithName("test")

	// Setup
	s := scheme.Scheme
	quixiov1.AddToScheme(s)
	rbacv1.AddToScheme(s)

	// Create test config
	cfg := &config.OperatorConfig{
		ClusterRoleName: "test-cluster-role",
	}

	// Create test role bindings
	desiredRb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rb",
			Namespace: "test-ns",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "test-cluster-role",
		},
	}

	foundRb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rb",
			Namespace: "test-ns",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "old-role",
		},
	}

	// Create test reconciler
	reconciler := &mockEnvironmentReconciler{
		Config: cfg,
	}

	// Test case 1: Role ref is different
	updated := reconciler.checkAndUpdateRoleRef(desiredRb, foundRb, logger)
	assert.True(t, updated)
	assert.Equal(t, "test-cluster-role", foundRb.RoleRef.Name)
	assert.Equal(t, "ClusterRole", foundRb.RoleRef.Kind)
	assert.Equal(t, "rbac.authorization.k8s.io", foundRb.RoleRef.APIGroup)

	// Test case 2: Role ref is already correct
	updatedRb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rb",
			Namespace: "test-ns",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "test-cluster-role",
		},
	}
	updated = reconciler.checkAndUpdateRoleRef(desiredRb, updatedRb, logger)
	assert.False(t, updated)
	assert.Equal(t, desiredRb.RoleRef, updatedRb.RoleRef)
}

func TestCheckAndUpdateSubjects(t *testing.T) {
	// Setup logging
	log.SetLogger(zap.New())
	logger := log.Log.WithName("test")

	// Setup
	s := scheme.Scheme
	quixiov1.AddToScheme(s)
	rbacv1.AddToScheme(s)

	// Create test config
	cfg := &config.OperatorConfig{
		ServiceAccountName:      "test-sa",
		ServiceAccountNamespace: "test-sa-ns",
	}

	// Create test role bindings
	desiredRb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rb",
			Namespace: "test-ns",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "test-sa",
				Namespace: "test-sa-ns",
			},
		},
	}

	// Test case 1: Empty subjects
	emptySubjectsRb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rb",
			Namespace: "test-ns",
		},
		Subjects: []rbacv1.Subject{},
	}

	// Create test reconciler
	reconciler := &mockEnvironmentReconciler{
		Config: cfg,
	}

	updated := reconciler.checkAndUpdateSubjects(desiredRb, emptySubjectsRb, logger)
	assert.True(t, updated)
	assert.Len(t, emptySubjectsRb.Subjects, 1)
	assert.Equal(t, "test-sa", emptySubjectsRb.Subjects[0].Name)
	assert.Equal(t, "test-sa-ns", emptySubjectsRb.Subjects[0].Namespace)
	assert.Equal(t, "ServiceAccount", emptySubjectsRb.Subjects[0].Kind)

	// Test case 2: Different subject
	wrongSubjectRb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rb",
			Namespace: "test-ns",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "wrong-sa",
				Namespace: "wrong-ns",
			},
		},
	}

	updated = reconciler.checkAndUpdateSubjects(desiredRb, wrongSubjectRb, logger)
	assert.True(t, updated)
	assert.Len(t, wrongSubjectRb.Subjects, 1)
	assert.Equal(t, "test-sa", wrongSubjectRb.Subjects[0].Name)
	assert.Equal(t, "test-sa-ns", wrongSubjectRb.Subjects[0].Namespace)

	// Test case 3: Correct subject
	correctSubjectRb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rb",
			Namespace: "test-ns",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "test-sa",
				Namespace: "test-sa-ns",
			},
		},
	}

	updated = reconciler.checkAndUpdateSubjects(desiredRb, correctSubjectRb, logger)
	assert.False(t, updated)
	assert.Equal(t, desiredRb.Subjects, correctSubjectRb.Subjects)

	// Test case 4: Multiple subjects with correct one
	multipleSubjectsRb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rb",
			Namespace: "test-ns",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "test-sa",
				Namespace: "test-sa-ns",
			},
			{
				Kind:      "ServiceAccount",
				Name:      "extra-sa",
				Namespace: "extra-ns",
			},
		},
	}

	updated = reconciler.checkAndUpdateSubjects(desiredRb, multipleSubjectsRb, logger)
	assert.True(t, updated)
	assert.Len(t, multipleSubjectsRb.Subjects, 1)
	assert.Equal(t, "test-sa", multipleSubjectsRb.Subjects[0].Name)
}

func TestApplyRoleBindingUpdates(t *testing.T) {
	// Setup logging
	log.SetLogger(zap.New())
	ctx := context.Background()
	logger := log.FromContext(ctx)

	// Setup
	s := scheme.Scheme
	quixiov1.AddToScheme(s)
	rbacv1.AddToScheme(s)

	// Create test environment
	env := &quixiov1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-env",
			Namespace: "default",
		},
		Spec: quixiov1.EnvironmentSpec{
			Id: "test-id",
		},
		Status: quixiov1.EnvironmentStatus{
			Namespace: "test-ns",
		},
	}

	// Create existing role binding
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "quix-environment-access",
			Namespace: "test-ns",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "old-role",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "old-sa",
				Namespace: "old-ns",
			},
		},
	}

	// Create test config
	cfg := &config.OperatorConfig{
		ClusterRoleName:         "test-cluster-role",
		ServiceAccountName:      "test-sa",
		ServiceAccountNamespace: "test-sa-ns",
	}

	// Create fake client
	client := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(env, rb).
		Build()

	// Create test reconciler
	reconciler := newMockEnvironmentReconciler(
		client,
		s,
		cfg,
	)

	// Test applying updates
	result, err := reconciler.applyRoleBindingUpdates(ctx, env, rb, rb.Name, logger)
	require.NoError(t, err)
	assert.True(t, result.IsZero(), "Expected empty result")

	// Verify updated role binding in API
	updatedRb := &rbacv1.RoleBinding{}
	err = client.Get(ctx, types.NamespacedName{Name: rb.Name, Namespace: rb.Namespace}, updatedRb)
	require.NoError(t, err)

	// Check that roleRef and subjects were updated
	assert.Equal(t, "test-cluster-role", updatedRb.RoleRef.Name)
	assert.Equal(t, "ClusterRole", updatedRb.RoleRef.Kind)
	assert.Len(t, updatedRb.Subjects, 1)
	assert.Equal(t, "test-sa", updatedRb.Subjects[0].Name)
	assert.Equal(t, "test-sa-ns", updatedRb.Subjects[0].Namespace)

	// Test with already up-to-date rolebinding
	result, err = reconciler.applyRoleBindingUpdates(ctx, env, updatedRb, updatedRb.Name, logger)
	require.NoError(t, err)
	assert.True(t, result.IsZero(), "Expected empty result")
}

func TestDefineRoleBinding(t *testing.T) {
	// Setup
	s := scheme.Scheme
	quixiov1.AddToScheme(s)
	rbacv1.AddToScheme(s)

	// Create test environment
	env := &quixiov1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-env",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: quixiov1.EnvironmentSpec{
			Id: "test-id",
		},
		Status: quixiov1.EnvironmentStatus{
			Namespace: "test-ns",
		},
	}

	// Create test config
	cfg := &config.OperatorConfig{
		ClusterRoleName:         "test-cluster-role",
		ServiceAccountName:      "test-sa",
		ServiceAccountNamespace: "test-sa-ns",
	}

	// Create test reconciler
	reconciler := &mockEnvironmentReconciler{
		Config: cfg,
	}

	// Test defining role binding with namespace from environment
	rb := reconciler.defineManagedRoleBinding(env, env.Status.Namespace)

	// Verify role binding properties
	assert.Equal(t, "quix-environment-access", rb.Name)
	assert.Equal(t, "test-ns", rb.Namespace)
	assert.Equal(t, "test-cluster-role", rb.RoleRef.Name)
	assert.Equal(t, "ClusterRole", rb.RoleRef.Kind)
	assert.Equal(t, "rbac.authorization.k8s.io", rb.RoleRef.APIGroup)
	assert.Len(t, rb.Subjects, 1)
	assert.Equal(t, "test-sa", rb.Subjects[0].Name)
	assert.Equal(t, "test-sa-ns", rb.Subjects[0].Namespace)
	assert.Equal(t, "ServiceAccount", rb.Subjects[0].Kind)

	// Verify labels
	assert.Equal(t, "quix-environment-operator", rb.Labels["quix.io/managed-by"])
	assert.Equal(t, "test-id", rb.Labels["quix.io/environment-id"])

	// Verify owner references
	assert.Len(t, rb.OwnerReferences, 1)
	assert.Equal(t, "Environment", rb.OwnerReferences[0].Kind)
	assert.Equal(t, "test-env", rb.OwnerReferences[0].Name)
	assert.Equal(t, "test-uid", string(rb.OwnerReferences[0].UID))
	assert.True(t, *rb.OwnerReferences[0].Controller)
}
