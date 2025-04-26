package namespaces

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
)

type mockStatusUpdater struct {
	mock.Mock
}

func (m *mockStatusUpdater) UpdateStatus(ctx context.Context, env *quixiov1.Environment, updates func(*quixiov1.EnvironmentStatus)) error {
	args := m.Called(mock.Anything, env, mock.Anything)
	if args.Get(0) != nil {
		return args.Error(0)
	}

	// Execute the update function
	if updates != nil {
		updates(&env.Status)
	}
	return nil
}

func (m *mockStatusUpdater) SetSuccessStatus(ctx context.Context, env *quixiov1.Environment, message string) error {
	args := m.Called(mock.Anything, env, message)
	return args.Error(0)
}

func (m *mockStatusUpdater) SetErrorStatus(ctx context.Context, env *quixiov1.Environment, phase quixiov1.EnvironmentPhase, err error, eventMsg string) error {
	// For testing simplicity, we don't care about exact error types
	// Use the generic mock.Anything for the error parameter
	args := m.Called(mock.Anything, env, phase, mock.Anything, eventMsg)
	return args.Error(0)
}

func TestApplyMetadata(t *testing.T) {
	tests := []struct {
		name           string
		environment    *quixiov1.Environment
		namespace      *corev1.Namespace
		expectedLabels map[string]string
		expectedAnnots map[string]string
		wantUpdate     bool
	}{
		{
			name: "Empty namespace",
			environment: &quixiov1.Environment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-env",
					Namespace: "default",
				},
				Spec: quixiov1.EnvironmentSpec{
					Id: "test-id",
				},
			},
			namespace:  &corev1.Namespace{},
			wantUpdate: true,
			expectedLabels: map[string]string{
				"quix.io/managed-by":     "quix-environment-operator",
				"quix.io/environment-id": "test-id",
			},
			expectedAnnots: map[string]string{
				"quix.io/created-by":                "quix-environment-operator",
				"quix.io/environment-id":            "test-id",
				"quix.io/environment-crd-namespace": "default",
				"quix.io/environment-resource-name": "test-env",
			},
		},
		{
			name: "Namespace with existing metadata",
			environment: &quixiov1.Environment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-env",
					Namespace: "default",
				},
				Spec: quixiov1.EnvironmentSpec{
					Id: "test-id",
					Labels: map[string]string{
						"custom-label": "custom-value",
					},
					Annotations: map[string]string{
						"custom-annotation": "custom-value",
					},
				},
			},
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"quix.io/managed-by":     "quix-environment-operator",
						"quix.io/environment-id": "test-id",
						"existing-label":         "existing-value",
					},
					Annotations: map[string]string{
						"quix.io/created-by":     "quix-environment-operator",
						"quix.io/environment-id": "test-id",
						"existing-annotation":    "existing-value",
					},
				},
			},
			wantUpdate: true,
			expectedLabels: map[string]string{
				"quix.io/managed-by":     "quix-environment-operator",
				"quix.io/environment-id": "test-id",
				"existing-label":         "existing-value",
				"custom-label":           "custom-value",
			},
			expectedAnnots: map[string]string{
				"quix.io/created-by":                "quix-environment-operator",
				"quix.io/environment-id":            "test-id",
				"quix.io/environment-crd-namespace": "default",
				"quix.io/environment-resource-name": "test-env",
				"existing-annotation":               "existing-value",
				"custom-annotation":                 "custom-value",
			},
		},
		{
			name: "No changes needed",
			environment: &quixiov1.Environment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-env",
					Namespace: "default",
				},
				Spec: quixiov1.EnvironmentSpec{
					Id: "test-id",
					Labels: map[string]string{
						"custom-label": "custom-value",
					},
					Annotations: map[string]string{
						"custom-annotation": "custom-value",
					},
				},
			},
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"quix.io/managed-by":     "quix-environment-operator",
						"quix.io/environment-id": "test-id",
						"custom-label":           "custom-value",
					},
					Annotations: map[string]string{
						"quix.io/created-by":                "quix-environment-operator",
						"quix.io/environment-id":            "test-id",
						"quix.io/environment-crd-namespace": "default",
						"quix.io/environment-resource-name": "test-env",
						"custom-annotation":                 "custom-value",
					},
				},
			},
			wantUpdate: false,
			expectedLabels: map[string]string{
				"quix.io/managed-by":     "quix-environment-operator",
				"quix.io/environment-id": "test-id",
				"custom-label":           "custom-value",
			},
			expectedAnnots: map[string]string{
				"quix.io/created-by":                "quix-environment-operator",
				"quix.io/environment-id":            "test-id",
				"quix.io/environment-crd-namespace": "default",
				"quix.io/environment-resource-name": "test-env",
				"custom-annotation":                 "custom-value",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup test manager
			manager := &DefaultNamespaceManager{}

			// Apply metadata
			gotUpdate := manager.ApplyMetadata(tt.environment, tt.namespace)

			// Check result
			assert.Equal(t, tt.wantUpdate, gotUpdate)
			assert.Equal(t, tt.expectedLabels, tt.namespace.Labels)
			assert.Equal(t, tt.expectedAnnots, tt.namespace.Annotations)
		})
	}
}

func TestIsNamespaceDeleted(t *testing.T) {
	manager := &DefaultNamespaceManager{}

	tests := []struct {
		name      string
		namespace *corev1.Namespace
		err       error
		want      bool
	}{
		{
			name:      "Namespace is nil",
			namespace: nil,
			err:       nil,
			want:      true,
		},
		{
			name:      "NotFound error",
			namespace: &corev1.Namespace{},
			err:       k8serrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "test-ns"),
			want:      true,
		},
		{
			name:      "Other error",
			namespace: &corev1.Namespace{},
			err:       errors.New("other error"),
			want:      false,
		},
		{
			name: "Namespace exists",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns",
				},
			},
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := manager.IsNamespaceDeleted(tt.namespace, tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsNamespaceManaged(t *testing.T) {
	manager := &DefaultNamespaceManager{}

	tests := []struct {
		name      string
		namespace *corev1.Namespace
		want      bool
	}{
		{
			name:      "Namespace is nil",
			namespace: nil,
			want:      false,
		},
		{
			name: "Labels are nil",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{},
			},
			want: false,
		},
		{
			name: "Missing managed-by label",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"other-label": "value",
					},
				},
			},
			want: false,
		},
		{
			name: "Wrong managed-by value",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"quix.io/managed-by": "other-operator",
					},
				},
			},
			want: false,
		},
		{
			name: "Correctly managed namespace",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"quix.io/managed-by": "quix-environment-operator",
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := manager.IsNamespaceManaged(tt.namespace)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestUpdateMetadata_Success(t *testing.T) {
	// Create test namespace and add it to the fake client
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-ns",
			Labels: map[string]string{
				"existing-label": "existing-value",
			},
		},
	}

	// Create fake client with the namespace
	client := fake.NewClientBuilder().
		WithObjects(namespace).
		Build()

	// Create test environment
	env := &quixiov1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-env",
			Namespace: "default",
		},
		Spec: quixiov1.EnvironmentSpec{
			Id: "test-id",
			Labels: map[string]string{
				"custom-label": "custom-value",
			},
		},
		Status: quixiov1.EnvironmentStatus{
			Phase: quixiov1.PhaseReady,
		},
	}

	// Create custom status updater for simple testing
	updater := &statusUpdaterForTesting{
		updateFunc: func(ctx context.Context, env *quixiov1.Environment, updates func(*quixiov1.EnvironmentStatus)) error {
			// Execute the update function on env
			if updates != nil {
				updates(&env.Status)
			}
			return nil
		},
		successFunc: func(ctx context.Context, env *quixiov1.Environment, message string) error {
			return nil
		},
		errorFunc: func(ctx context.Context, env *quixiov1.Environment, phase quixiov1.EnvironmentPhase, err error, eventMsg string) error {
			return err
		},
	}

	// Create recorder
	recorder := record.NewFakeRecorder(10)

	// Create namespace manager
	manager := &DefaultNamespaceManager{
		Client:        client,
		Recorder:      recorder,
		StatusUpdater: updater,
	}

	// Test with namespace that needs updates
	ctx := context.Background()
	err := manager.UpdateMetadata(ctx, env, namespace)

	// Verify results
	assert.NoError(t, err)
	assert.Contains(t, namespace.Labels, "quix.io/managed-by")
	assert.Contains(t, namespace.Labels, "custom-label")

	// Verify the namespace was updated in the fake client
	updatedNs := &corev1.Namespace{}
	err = client.Get(ctx, types.NamespacedName{Name: "test-ns"}, updatedNs)
	assert.NoError(t, err)
	assert.Equal(t, "quix-environment-operator", updatedNs.Labels["quix.io/managed-by"])
	assert.Equal(t, "custom-value", updatedNs.Labels["custom-label"])
}

func TestUpdateMetadata_Error(t *testing.T) {
	// Create fake client with no namespace
	client := fake.NewClientBuilder().Build()

	// Create test namespace (not in the client, will cause not found on update)
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-ns",
			Labels: map[string]string{
				"existing-label": "existing-value",
			},
		},
	}

	// Create test environment
	env := &quixiov1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-env",
			Namespace: "default",
		},
		Spec: quixiov1.EnvironmentSpec{
			Id: "test-id",
			Labels: map[string]string{
				"custom-label": "custom-value",
			},
		},
		Status: quixiov1.EnvironmentStatus{
			Phase: quixiov1.PhaseReady,
		},
	}

	// Expected error
	var gotError error

	// Create custom status updater for simple testing
	updater := &statusUpdaterForTesting{
		updateFunc: func(ctx context.Context, env *quixiov1.Environment, updates func(*quixiov1.EnvironmentStatus)) error {
			// Execute the update function on env
			if updates != nil {
				updates(&env.Status)
			}
			return nil
		},
		successFunc: func(ctx context.Context, env *quixiov1.Environment, message string) error {
			return nil
		},
		errorFunc: func(ctx context.Context, env *quixiov1.Environment, phase quixiov1.EnvironmentPhase, err error, eventMsg string) error {
			// Capture the error that would be sent to SetErrorStatus
			gotError = err
			assert.Equal(t, quixiov1.PhaseUpdateFailed, phase)
			assert.True(t, k8serrors.IsNotFound(err), "Expected NotFound error in SetErrorStatus call")
			return err
		},
	}

	// Create recorder
	recorder := record.NewFakeRecorder(10)

	// Create namespace manager with custom client for error scenario
	manager := &DefaultNamespaceManager{
		Client:        client,
		Recorder:      recorder,
		StatusUpdater: updater,
	}

	// Test update that will fail
	ctx := context.Background()
	err := manager.UpdateMetadata(ctx, env, namespace)

	// Error should be returned
	assert.Error(t, err)
	assert.True(t, k8serrors.IsNotFound(err), "Expected NotFound error")
	assert.NotNil(t, gotError, "Expected error to be passed to SetErrorStatus")
}

// Simple status updater implementation for testing
type statusUpdaterForTesting struct {
	updateFunc  func(context.Context, *quixiov1.Environment, func(*quixiov1.EnvironmentStatus)) error
	successFunc func(context.Context, *quixiov1.Environment, string) error
	errorFunc   func(context.Context, *quixiov1.Environment, quixiov1.EnvironmentPhase, error, string) error
}

func (s *statusUpdaterForTesting) UpdateStatus(ctx context.Context, env *quixiov1.Environment, updates func(*quixiov1.EnvironmentStatus)) error {
	return s.updateFunc(ctx, env, updates)
}

func (s *statusUpdaterForTesting) SetSuccessStatus(ctx context.Context, env *quixiov1.Environment, message string) error {
	return s.successFunc(ctx, env, message)
}

func (s *statusUpdaterForTesting) SetErrorStatus(ctx context.Context, env *quixiov1.Environment, phase quixiov1.EnvironmentPhase, err error, eventMsg string) error {
	return s.errorFunc(ctx, env, phase, err, eventMsg)
}

// Mock client implementation for error testing
type mockClient struct {
	client.Client
	updateErr error
}

func (c *mockClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if c.updateErr != nil {
		return c.updateErr
	}
	return c.Client.Update(ctx, obj, opts...)
}
