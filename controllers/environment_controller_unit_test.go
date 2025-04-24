// Unit tests for isolated functions with mocked dependencies.
// Fast, targeted verification of specific code paths and edge cases.

package controllers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
	"github.com/quix-analytics/quix-environment-operator/internal/namespaces"
	"github.com/quix-analytics/quix-environment-operator/internal/status"
)

func TestValidateEnvironment(t *testing.T) {
	// Register the custom resource
	s := scheme.Scheme
	s.AddKnownTypes(quixiov1.GroupVersion, &quixiov1.Environment{})

	// Create a mock client
	client := fake.NewClientBuilder().WithScheme(s).Build()

	// Create the reconciler with a mock recorder
	recorder := record.NewFakeRecorder(10)

	// Create the namespace manager mock
	nsManager := &namespaces.MockNamespaceManager{}

	// Create status updater mock
	statusUpdater := &status.MockStatusUpdater{}

	reconciler, err := NewEnvironmentReconciler(
		client,
		s,
		recorder,
		&config.OperatorConfig{
			NamespaceSuffix:         "-suffix",
			ServiceAccountName:      "test-sa",
			ServiceAccountNamespace: "test-ns",
			ClusterRoleName:         "test-role",
		},
		nsManager,
		statusUpdater,
	)
	assert.NoError(t, err)

	tests := []struct {
		name        string
		environment *quixiov1.Environment
		wantErr     bool
		errContains string
	}{
		{
			name: "Valid environment",
			environment: &quixiov1.Environment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-env",
					Namespace: "default",
				},
				Spec: quixiov1.EnvironmentSpec{
					Id: "test",
				},
			},
			wantErr: false,
		},
		{
			name: "Empty name",
			environment: &quixiov1.Environment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-env",
					Namespace: "default",
				},
				Spec: quixiov1.EnvironmentSpec{
					Id: "",
				},
			},
			wantErr:     true,
			errContains: "id is required",
		},
		{
			name: "Too long name",
			environment: &quixiov1.Environment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-env",
					Namespace: "default",
				},
				Spec: quixiov1.EnvironmentSpec{
					Id: "this-is-a-very-long-id-that-will-exceed-the-kubernetes-namespace-name-length-limit",
				},
			},
			wantErr:     true,
			errContains: "exceeds the 63 character limit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reconciler.validateEnvironment(context.Background(), tt.environment)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSetCondition(t *testing.T) {
	// Register the custom resource
	s := scheme.Scheme
	s.AddKnownTypes(quixiov1.GroupVersion, &quixiov1.Environment{})

	// Create a test environment
	env := &quixiov1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-env",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: quixiov1.EnvironmentSpec{
			Id: "test",
		},
		Status: quixiov1.EnvironmentStatus{},
	}

	// Create a mock client with the environment
	mockClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(env).
		WithStatusSubresource(&quixiov1.Environment{}).
		Build()

	// Create a recorder for events
	recorder := record.NewFakeRecorder(10)

	// Create the StatusUpdater
	statusUpdater := status.NewStatusUpdater(mockClient, recorder)

	// Create the reconciler with the StatusUpdater
	reconciler, err := NewEnvironmentReconciler(
		mockClient,
		s,
		recorder,
		&config.OperatorConfig{
			NamespaceSuffix:         "-suffix",
			ServiceAccountName:      "test-sa",
			ServiceAccountNamespace: "test-ns",
			ClusterRoleName:         "test-role",
		},
		&namespaces.MockNamespaceManager{},
		statusUpdater,
	)
	assert.NoError(t, err)

	// Test adding a condition
	condition := metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "Testing",
		Message: "This is a test condition",
	}

	// Use StatusUpdater's SetSuccessStatus which will set the condition
	reconciler.StatusUpdater().SetSuccessStatus(context.Background(), env, condition.Type, condition.Message)

	// Verify the condition was set
	updatedEnv := &quixiov1.Environment{}
	err = mockClient.Get(context.Background(), types.NamespacedName{
		Name:      env.Name,
		Namespace: env.Namespace,
	}, updatedEnv)
	assert.NoError(t, err)

	// Check if a condition was added (note: may not exactly match our input since SetSuccessStatus creates its own)
	assert.NotEmpty(t, updatedEnv.Status.Conditions, "Expected at least one condition to be set")

	// Find the Ready condition
	var readyCondition *metav1.Condition
	for i := range updatedEnv.Status.Conditions {
		if updatedEnv.Status.Conditions[i].Type == "Ready" {
			readyCondition = &updatedEnv.Status.Conditions[i]
			break
		}
	}

	assert.NotNil(t, readyCondition, "Expected to find a Ready condition")
	assert.Equal(t, metav1.ConditionTrue, readyCondition.Status)
	assert.Equal(t, "This is a test condition", readyCondition.Message)
	assert.Equal(t, int64(1), readyCondition.ObservedGeneration)
	assert.False(t, readyCondition.LastTransitionTime.IsZero())

	// Test setting an error condition
	testErr := errors.New("test error")

	// Use StatusUpdater's SetErrorStatus
	reconciler.StatusUpdater().SetErrorStatus(
		context.Background(),
		updatedEnv,
		quixiov1.PhaseCreateFailed,
		"Ready",
		testErr,
		"Test error occurred",
	)

	// Verify the condition was updated
	finalEnv := &quixiov1.Environment{}
	err = mockClient.Get(context.Background(), types.NamespacedName{
		Name:      env.Name,
		Namespace: env.Namespace,
	}, finalEnv)
	assert.NoError(t, err)

	// Find the updated Ready condition
	readyCondition = nil
	for i := range finalEnv.Status.Conditions {
		if finalEnv.Status.Conditions[i].Type == "Ready" {
			readyCondition = &finalEnv.Status.Conditions[i]
			break
		}
	}

	assert.NotNil(t, readyCondition, "Expected to find a Ready condition")
	assert.Equal(t, metav1.ConditionFalse, readyCondition.Status)
	assert.Contains(t, readyCondition.Message, "Test error occurred")
	assert.Contains(t, readyCondition.Message, "test error")
}

func TestUpdateEnvironmentStatus(t *testing.T) {
	// Register the custom resource
	s := scheme.Scheme
	s.AddKnownTypes(quixiov1.GroupVersion, &quixiov1.Environment{})

	env := &quixiov1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-env",
			Namespace: "default",
		},
		Status: quixiov1.EnvironmentStatus{},
	}

	tests := []struct {
		name          string
		setupMocks    func(*status.MockStatusUpdater)
		updateFn      func(*quixiov1.EnvironmentStatus)
		expectedError bool
	}{
		{
			name: "Successful status update",
			setupMocks: func(mockStatusUpdater *status.MockStatusUpdater) {
				// No specific mock function needed for success,
				// default behavior of MockStatusUpdater applies updates.
			},
			updateFn: func(status *quixiov1.EnvironmentStatus) {
				status.Phase = quixiov1.PhaseReady
				status.Namespace = "test-namespace"
			},
			expectedError: false,
		},
		{
			name: "Error during status update",
			setupMocks: func(mockStatusUpdater *status.MockStatusUpdater) {
				mockStatusUpdater.UpdateStatusFunc = func(ctx context.Context, env *quixiov1.Environment, updates func(*quixiov1.EnvironmentStatus)) error {
					// Don't apply updates, just return error
					return errors.New("update error")
				}
			},
			updateFn: func(status *quixiov1.EnvironmentStatus) {
				status.Phase = quixiov1.PhaseReady
			},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create the mock status updater
			mockStatusUpdater := &status.MockStatusUpdater{}
			tt.setupMocks(mockStatusUpdater)

			// Create a fake client
			mockClient := fake.NewClientBuilder().WithScheme(s).Build()

			// Create a mock namespace manager
			mockNamespaceManager := &namespaces.MockNamespaceManager{}

			// Create the reconciler with the constructor
			reconciler, err := NewEnvironmentReconciler(
				mockClient,
				s,
				record.NewFakeRecorder(10),
				&config.OperatorConfig{},
				mockNamespaceManager,
				mockStatusUpdater,
			)
			assert.NoError(t, err)

			// Call the method to test
			// Create a deep copy to avoid modifying the original env between test runs if UpdateStatusFunc doesn't error
			envCopy := env.DeepCopy()
			err = reconciler.StatusUpdater().UpdateStatus(context.Background(), envCopy, tt.updateFn)

			// Check results
			if tt.expectedError {
				assert.Error(t, err)
				assert.Equal(t, "update error", err.Error()) // Check specific error
			} else {
				assert.NoError(t, err)
				// Verify updates were applied if successful
				expectedEnv := env.DeepCopy() // Create a copy to apply expected updates
				tt.updateFn(&expectedEnv.Status)
				assert.Equal(t, expectedEnv.Status, envCopy.Status, "Status should be updated on success")
			}
		})
	}
}

func TestCreateNamespace(t *testing.T) {
	// Register the custom resource
	s := scheme.Scheme
	s.AddKnownTypes(quixiov1.GroupVersion, &quixiov1.Environment{})

	// Define test cases
	tests := []struct {
		name              string
		environmentSpec   quixiov1.EnvironmentSpec
		initialObjects    []client.Object
		setupClient       func(*fake.ClientBuilder) *fake.ClientBuilder
		setupMocks        func(*namespaces.MockNamespaceManager)
		expectError       bool
		expectLabels      map[string]string
		expectAnnotations map[string]string
	}{
		{
			name: "Successful namespace creation",
			environmentSpec: quixiov1.EnvironmentSpec{
				Id: "test",
			},
			initialObjects: []client.Object{}, // No initial objects needed for creation
			setupClient: func(builder *fake.ClientBuilder) *fake.ClientBuilder {
				return builder // No special client setup needed
			},
			setupMocks: func(m *namespaces.MockNamespaceManager) {
				m.ApplyMetadataFunc = func(env *quixiov1.Environment, ns *corev1.Namespace) bool {
					// Apply expected labels and annotations based on test case
					if ns.Labels == nil {
						ns.Labels = make(map[string]string)
					}
					if ns.Annotations == nil {
						ns.Annotations = make(map[string]string)
					}
					ns.Labels["quix.io/managed-by"] = "quix-environment-operator"
					ns.Labels["quix.io/environment-id"] = "test"
					ns.Annotations["quix.io/created-by"] = "quix-environment-operator"
					ns.Annotations["quix.io/environment-id"] = "test"
					ns.Annotations["quix.io/environment-crd-namespace"] = "default"  // Assuming default ns for CR
					ns.Annotations["quix.io/environment-resource-name"] = "test-env" // Assuming this name for CR
					return true
				}
			},
			expectError: false,
			expectLabels: map[string]string{
				"quix.io/managed-by":     "quix-environment-operator",
				"quix.io/environment-id": "test",
			},
			expectAnnotations: map[string]string{
				"quix.io/created-by":                "quix-environment-operator",
				"quix.io/environment-id":            "test",
				"quix.io/environment-crd-namespace": "default",
				"quix.io/environment-resource-name": "test-env",
			},
		},
		{
			name: "Namespace creation with custom labels and annotations",
			environmentSpec: quixiov1.EnvironmentSpec{
				Id: "test",
				Labels: map[string]string{
					"custom-label": "value",
				},
				Annotations: map[string]string{
					"custom-annotation": "value",
				},
			},
			initialObjects: []client.Object{},
			setupClient: func(builder *fake.ClientBuilder) *fake.ClientBuilder {
				return builder
			},
			setupMocks: func(m *namespaces.MockNamespaceManager) {
				m.ApplyMetadataFunc = func(env *quixiov1.Environment, ns *corev1.Namespace) bool {
					if ns.Labels == nil {
						ns.Labels = make(map[string]string)
					}
					if ns.Annotations == nil {
						ns.Annotations = make(map[string]string)
					}
					ns.Labels["quix.io/managed-by"] = "quix-environment-operator"
					ns.Labels["quix.io/environment-id"] = "test"
					ns.Labels["custom-label"] = "value" // Add custom
					ns.Annotations["quix.io/created-by"] = "quix-environment-operator"
					ns.Annotations["quix.io/environment-id"] = "test"
					ns.Annotations["quix.io/environment-crd-namespace"] = "default"
					ns.Annotations["quix.io/environment-resource-name"] = "test-env"
					ns.Annotations["custom-annotation"] = "value" // Add custom
					return true
				}
			},
			expectError: false,
			expectLabels: map[string]string{
				"quix.io/managed-by":     "quix-environment-operator",
				"quix.io/environment-id": "test",
				"custom-label":           "value",
			},
			expectAnnotations: map[string]string{
				"quix.io/created-by":                "quix-environment-operator",
				"quix.io/environment-id":            "test",
				"quix.io/environment-crd-namespace": "default",
				"quix.io/environment-resource-name": "test-env",
				"custom-annotation":                 "value",
			},
		},
		{
			name: "Error creating namespace - API error",
			environmentSpec: quixiov1.EnvironmentSpec{
				Id: "test",
			},
			initialObjects: []client.Object{},
			setupClient: func(builder *fake.ClientBuilder) *fake.ClientBuilder {
				// NOTE: Using interceptor is generally preferred over reactor
				builder.WithInterceptorFuncs(interceptor.Funcs{
					Create: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						if _, ok := obj.(*corev1.Namespace); ok && obj.GetName() == "test-suffix" {
							return errors.New("create error")
						}
						return client.Create(ctx, obj, opts...)
					},
				})
				return builder
			},
			setupMocks: func(m *namespaces.MockNamespaceManager) {
				// ApplyMetadata will still be called
				m.ApplyMetadataFunc = func(env *quixiov1.Environment, ns *corev1.Namespace) bool {
					return true // Simulate metadata applied before the failed create
				}
			},
			expectError: true,
		},
		{
			name: "Namespace already exists",
			environmentSpec: quixiov1.EnvironmentSpec{
				Id: "test",
			},
			initialObjects: []client.Object{
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-suffix"}}, // Pre-existing NS
			},
			setupClient: func(builder *fake.ClientBuilder) *fake.ClientBuilder {
				return builder // Client will find the existing NS on Get
			},
			setupMocks: func(m *namespaces.MockNamespaceManager) {
				// ApplyMetadata should not be called if NS already exists
			},
			expectError: false, // Not an error, it's idempotent
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create base client builder
			clientBuilder := fake.NewClientBuilder().WithScheme(s).WithObjects(tt.initialObjects...)
			// Apply test-specific client setup
			clientBuilder = tt.setupClient(clientBuilder)
			mockClient := clientBuilder.Build()

			// Create mock namespace manager and apply setup
			mockNamespaceManager := &namespaces.MockNamespaceManager{}
			if tt.setupMocks != nil {
				tt.setupMocks(mockNamespaceManager)
			}

			// Create the test environment
			env := &quixiov1.Environment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-env",
					Namespace: "default", // Consistent namespace for CR
				},
				Spec: tt.environmentSpec,
			}

			// Create the reconciler with constructor
			reconciler, err := NewEnvironmentReconciler(
				mockClient,
				s,
				record.NewFakeRecorder(10),
				&config.OperatorConfig{
					NamespaceSuffix: "-suffix",
				},
				mockNamespaceManager,
				&status.MockStatusUpdater{},
			)
			assert.NoError(t, err)

			// Call createNamespace
			err = reconciler.createNamespace(context.Background(), env, "test-suffix")

			// Check the result
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				// Verify the namespace exists and has correct metadata if created
				if !tt.expectError && len(tt.initialObjects) == 0 { // Only check if NS was expected to be created
					createdNs := &corev1.Namespace{}
					getErr := mockClient.Get(context.Background(), types.NamespacedName{Name: "test-suffix"}, createdNs)
					assert.NoError(t, getErr, "Namespace should exist after successful creation")
					assert.Equal(t, tt.expectLabels, createdNs.Labels)
					assert.Equal(t, tt.expectAnnotations, createdNs.Annotations)
				}
			}
		})
	}
}

func TestHandleDeletion_NamespaceAlreadyDeleted(t *testing.T) {
	// Register the custom resource
	s := scheme.Scheme
	s.AddKnownTypes(quixiov1.GroupVersion, &quixiov1.Environment{})

	// Create a test environment
	deletionTs := metav1.Now()
	env := &quixiov1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-env",
			Namespace:         "default",
			DeletionTimestamp: &deletionTs, // Mark CR for deletion
			Finalizers: []string{
				EnvironmentFinalizer,
			},
		},
		Spec: quixiov1.EnvironmentSpec{
			Id: "test",
		},
		Status: quixiov1.EnvironmentStatus{
			Phase: quixiov1.PhaseDeleting,
		},
	}

	// Create a mock client
	mockClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(env). // Start with the env CR
		WithStatusSubresource(&quixiov1.Environment{}).
		// Simulate NotFound for the namespace Get using an interceptor
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Namespace); ok && key.Name == "test-suffix" {
					return k8serrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, key.Name)
				}
				// Fallback to default behavior for other Gets (like the Environment itself)
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	// Create the namespace manager mock
	nsManager := &namespaces.MockNamespaceManager{} // Use dedicated mock

	// Create status updater mock
	statusUpdater := &status.MockStatusUpdater{} // Use dedicated mock
	var successStatusCalled bool
	statusUpdater.SetSuccessStatusFunc = func(ctx context.Context, env *quixiov1.Environment, conditionType, message string) {
		assert.Equal(t, status.ConditionTypeNamespaceDeleted, conditionType) // Use constant
		assert.Equal(t, "Namespace deletion completed", message)
		successStatusCalled = true
	}

	// Create the reconciler with constructor
	reconciler, err := NewEnvironmentReconciler(
		mockClient, // Use fake client
		s,
		record.NewFakeRecorder(10),
		&config.OperatorConfig{
			NamespaceSuffix: "-suffix",
		},
		nsManager,     // Use dedicated mock
		statusUpdater, // Use dedicated mock
	)
	assert.NoError(t, err)

	// Call handleDeletion
	result, err := reconciler.handleDeletion(context.Background(), env)

	// Check the result
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result) // Finalizer removed, no requeue
	assert.True(t, successStatusCalled, "SetSuccessStatus should be called")

	// Verify the finalizer was removed
	updatedEnv := &quixiov1.Environment{}
	err = mockClient.Get(context.Background(), types.NamespacedName{Name: "test-env", Namespace: "default"}, updatedEnv)
	assert.Error(t, err, "not found")
}

// Define DefaultRequeueTime if not globally available in tests
const DefaultRequeueTime = 5 * time.Second

func TestHandleDeletion_DeleteNamespaceError(t *testing.T) {
	// Register the custom resource
	s := scheme.Scheme
	s.AddKnownTypes(quixiov1.GroupVersion, &quixiov1.Environment{})

	// Create a test environment
	deletionTs := metav1.Now()
	env := &quixiov1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-env",
			Namespace:         "default",
			DeletionTimestamp: &deletionTs, // Mark CR for deletion
			Finalizers: []string{
				EnvironmentFinalizer,
			},
		},
		Spec: quixiov1.EnvironmentSpec{
			Id: "test",
		},
		Status: quixiov1.EnvironmentStatus{
			Phase: quixiov1.PhaseDeleting,
		},
	}

	// Create a namespace *without* deletion timestamp
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-suffix",
			Labels: map[string]string{ // Add managed label so controller tries to delete it
				ManagedByLabel: OperatorName,
			},
		},
	}

	// Create a client that simulates an error during namespace deletion
	mockClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(env, namespace).
		WithStatusSubresource(&quixiov1.Environment{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*corev1.Namespace); ok && obj.GetName() == "test-suffix" {
					return errors.New("delete error") // Simulate delete failure
				}
				return cl.Delete(ctx, obj, opts...)
			},
			// Handle Get for env and namespace
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	// Create the namespace manager mock
	nsManager := &namespaces.MockNamespaceManager{} // Use dedicated mock

	// Create status updater mock
	statusUpdater := &status.MockStatusUpdater{} // Use dedicated mock
	// We don't expect SetErrorStatus in handleDeletion for delete errors, it just returns the error.

	// Create the reconciler with constructor
	reconciler, err := NewEnvironmentReconciler(
		mockClient, // Use fake client
		s,
		record.NewFakeRecorder(10),
		&config.OperatorConfig{
			NamespaceSuffix: "-suffix",
		},
		nsManager,     // Use dedicated mock
		statusUpdater, // Use dedicated mock
	)
	assert.NoError(t, err)

	// Call handleDeletion
	result, err := reconciler.handleDeletion(context.Background(), env)

	// Check the result
	assert.Error(t, err) // Expect the error from the delete call
	assert.ErrorContains(t, err, "delete error")
	// The controller returns the error, which causes requeue by default manager settings
	assert.Equal(t, ctrl.Result{RequeueAfter: 5 * time.Second}, result) // Expect RequeueAfter from error

	// Verify the finalizer is *NOT* removed because deletion failed
	updatedEnv := &quixiov1.Environment{}
	err = mockClient.Get(context.Background(), types.NamespacedName{Name: "test-env", Namespace: "default"}, updatedEnv)
	assert.NoError(t, err) // Get should still work
	assert.Contains(t, updatedEnv.Finalizers, EnvironmentFinalizer)
}

func TestHandleDeletion_RemoveFinalizerError(t *testing.T) {
	// Register the custom resource
	s := scheme.Scheme
	s.AddKnownTypes(quixiov1.GroupVersion, &quixiov1.Environment{})

	// Create a test environment (namespace assumed deleted)
	deletionTs := metav1.Now()
	env := &quixiov1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-env",
			Namespace:         "default",
			DeletionTimestamp: &deletionTs,
			Finalizers: []string{
				EnvironmentFinalizer,
			},
		},
		Spec: quixiov1.EnvironmentSpec{Id: "test"},
	}

	// Create a client that simulates an error during finalizer removal (Update)
	mockClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(env).
		WithStatusSubresource(&quixiov1.Environment{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Namespace); ok && key.Name == "test-suffix" {
					// Simulate namespace is gone
					return k8serrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, key.Name)
				}
				// Handle Get for Environment CR itself
				return cl.Get(ctx, key, obj, opts...)
			},
			Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if updatedEnv, ok := obj.(*quixiov1.Environment); ok && updatedEnv.Name == "test-env" {
					// Check if finalizer removal is attempted (only fail on the update that removes it)
					if len(updatedEnv.Finalizers) == 0 {
						// Fetch original env from the fake client store to compare
						originalEnv := &quixiov1.Environment{}
						_ = cl.Get(ctx, types.NamespacedName{Name: "test-env", Namespace: "default"}, originalEnv) // Ignore error for simplicity
						if len(originalEnv.Finalizers) > 0 {
							return errors.New("update finalizer error") // Simulate update failure specifically for finalizer removal
						}
					}
				}
				return cl.Update(ctx, obj, opts...)
			},
		}).
		Build()

	// Create the namespace manager mock
	nsManager := &namespaces.MockNamespaceManager{}

	// Create status updater mock
	statusUpdater := &status.MockStatusUpdater{}
	// SetSuccessStatus should still be called before the finalizer update fails
	var successCalled bool
	statusUpdater.SetSuccessStatusFunc = func(ctx context.Context, env *quixiov1.Environment, conditionType, message string) {
		successCalled = true
	}
	// SetErrorStatus is NOT called by handleDeletion on finalizer update failure, it returns the error

	// Create the reconciler
	reconciler, err := NewEnvironmentReconciler(
		mockClient, s, record.NewFakeRecorder(10),
		&config.OperatorConfig{NamespaceSuffix: "-suffix"},
		nsManager, statusUpdater,
	)
	assert.NoError(t, err)

	// Call handleDeletion
	result, err := reconciler.handleDeletion(context.Background(), env)

	// Check the result
	assert.Error(t, err) // Expect the error from the finalizer update
	assert.ErrorContains(t, err, "update finalizer error")
	// Expect RequeueAfter based on the returned error
	assert.Equal(t, ctrl.Result{RequeueAfter: 2 * time.Second}, result) // Requeue based on error path in handleDeletion
	assert.True(t, successCalled, "SetSuccessStatus should be called before finalizer update attempt")

	// Verify the finalizer is *NOT* removed because update failed
	finalEnv := &quixiov1.Environment{}
	getErr := mockClient.Get(context.Background(), types.NamespacedName{Name: "test-env", Namespace: "default"}, finalEnv)
	assert.NoError(t, getErr) // Get should still work
	assert.Contains(t, finalEnv.Finalizers, EnvironmentFinalizer)
}
