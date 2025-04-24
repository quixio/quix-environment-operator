// Unit tests for isolated functions with mocked dependencies.
// Fast, targeted verification of specific code paths and edge cases.

package controllers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
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
	nsManager := &MockNamespaceManager{}

	// Create status updater mock
	statusUpdater := &MockStatusUpdater{}

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
	client := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(env).
		WithStatusSubresource(&quixiov1.Environment{}).
		Build()

	// Create a recorder for events
	recorder := record.NewFakeRecorder(10)

	// Create the StatusUpdater
	statusUpdater := status.NewStatusUpdater(client, recorder)

	// Create the reconciler with the StatusUpdater
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
		&MockNamespaceManager{},
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
	err = client.Get(context.Background(), types.NamespacedName{
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
	err = client.Get(context.Background(), types.NamespacedName{
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
		setupMock     func(*MockStatusUpdater)
		updateFn      func(*quixiov1.EnvironmentStatus)
		expectedError bool
	}{
		{
			name: "Successful status update",
			setupMock: func(mockStatusUpdater *MockStatusUpdater) {
				mockStatusUpdater.On("UpdateStatus",
					mock.MatchedBy(func(ctx context.Context) bool { return true }),
					mock.MatchedBy(func(env *quixiov1.Environment) bool { return true }),
					mock.AnythingOfType("func(*v1.EnvironmentStatus)")).
					Run(func(args mock.Arguments) {
						// Extract the update function and apply it to the environment's status
						env := args.Get(1).(*quixiov1.Environment)
						updateFn := args.Get(2).(func(*quixiov1.EnvironmentStatus))
						updateFn(&env.Status)
					}).
					Return(nil)
			},
			updateFn: func(status *quixiov1.EnvironmentStatus) {
				status.Phase = quixiov1.PhaseReady
				status.Namespace = "test-namespace"
			},
			expectedError: false,
		},
		{
			name: "Error during status update",
			setupMock: func(mockStatusUpdater *MockStatusUpdater) {
				mockStatusUpdater.On("UpdateStatus",
					mock.MatchedBy(func(ctx context.Context) bool { return true }),
					mock.MatchedBy(func(env *quixiov1.Environment) bool { return true }),
					mock.AnythingOfType("func(*v1.EnvironmentStatus)")).
					Return(errors.New("update error"))
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
			mockStatusUpdater := &MockStatusUpdater{}
			tt.setupMock(mockStatusUpdater)

			// Create a mock client
			mockClient := &MockClient{}

			// Create a mock namespace manager
			mockNamespaceManager := &MockNamespaceManager{}

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
			err = reconciler.StatusUpdater().UpdateStatus(context.Background(), env, tt.updateFn)

			// Check results
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockStatusUpdater.AssertExpectations(t)
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
		mockClientCreate  func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error
		expectError       bool
		expectLabels      map[string]string
		expectAnnotations map[string]string
	}{
		{
			name: "Successful namespace creation",
			environmentSpec: quixiov1.EnvironmentSpec{
				Id: "test",
			},
			mockClientCreate: func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
				return nil
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
			mockClientCreate: func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
				return nil
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
			name: "Error creating namespace",
			environmentSpec: quixiov1.EnvironmentSpec{
				Id: "test",
			},
			mockClientCreate: func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
				return errors.New("create error")
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock client
			mockClient := &MockClient{}

			// Set up the mock client's Get function to simulate checking if namespace already exists
			mockClient.On("Get", mock.Anything, types.NamespacedName{Name: "test-suffix"}, mock.AnythingOfType("*v1.Namespace")).
				Return(k8serrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "test-suffix"))

			// Set up the mock client's Create function
			mockClient.On("Create", mock.Anything, mock.AnythingOfType("*v1.Namespace"), mock.Anything).
				Run(func(args mock.Arguments) {
					// Get the namespace being created
					ns := args.Get(1).(*corev1.Namespace)

					// Verify the namespace has the expected name
					assert.Equal(t, "test-suffix", ns.Name)

					// Verify labels and annotations if this is a success case
					if !tt.expectError {
						// Check that all expected labels are present
						for k, v := range tt.expectLabels {
							assert.Equal(t, v, ns.Labels[k])
						}

						// Check that all expected annotations are present
						for k, v := range tt.expectAnnotations {
							assert.Equal(t, v, ns.Annotations[k])
						}
					}
				}).
				Return(tt.mockClientCreate(context.Background(), nil))

			// Create a mock namespace manager
			mockNamespaceManager := &MockNamespaceManager{}
			mockNamespaceManager.On("ApplyMetadata", mock.Anything, mock.Anything).
				Run(func(args mock.Arguments) {
					env := args.Get(0).(*quixiov1.Environment)
					namespace := args.Get(1).(*corev1.Namespace)

					// Apply expected labels and annotations
					if namespace.Labels == nil {
						namespace.Labels = make(map[string]string)
					}
					if namespace.Annotations == nil {
						namespace.Annotations = make(map[string]string)
					}

					// Apply required labels
					namespace.Labels["quix.io/managed-by"] = "quix-environment-operator"
					namespace.Labels["quix.io/environment-id"] = env.Spec.Id

					// Apply custom labels
					for k, v := range env.Spec.Labels {
						namespace.Labels[k] = v
					}

					// Apply required annotations
					namespace.Annotations["quix.io/created-by"] = "quix-environment-operator"
					namespace.Annotations["quix.io/environment-id"] = env.Spec.Id
					namespace.Annotations["quix.io/environment-crd-namespace"] = env.Namespace
					namespace.Annotations["quix.io/environment-resource-name"] = env.Name

					// Apply custom annotations
					for k, v := range env.Spec.Annotations {
						namespace.Annotations[k] = v
					}
				}).
				Return(true)

			// Create the test environment
			env := &quixiov1.Environment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-env",
					Namespace: "default",
				},
				Spec: tt.environmentSpec,
			}

			// Create the reconciler with constructor
			reconciler, err := NewEnvironmentReconciler(
				mockClient,
				scheme.Scheme,
				record.NewFakeRecorder(10),
				&config.OperatorConfig{
					NamespaceSuffix: "-suffix",
				},
				mockNamespaceManager,
				&MockStatusUpdater{},
			)
			assert.NoError(t, err)

			// Call createNamespace
			err = reconciler.createNamespace(context.Background(), env, "test-suffix")

			// Check the result
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Verify that all expected method calls were made
			mockClient.AssertExpectations(t)
			mockNamespaceManager.AssertExpectations(t)
		})
	}
}

func TestHandleDeletion_NamespaceAlreadyDeleted(t *testing.T) {
	// Register the custom resource
	s := scheme.Scheme
	s.AddKnownTypes(quixiov1.GroupVersion, &quixiov1.Environment{})

	// Create a test environment
	env := &quixiov1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-env",
			Namespace: "default",
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
	client := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(env).
		WithStatusSubresource(&quixiov1.Environment{}).
		Build()

	// Configure the mock client to return NotFound for namespace
	mockClient := &MockClient{Client: client}
	mockClient.On("Get", mock.Anything, mock.AnythingOfType("types.NamespacedName"), mock.AnythingOfType("*v1.Namespace")).
		Return(k8serrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "test-suffix"))

	// For updating the Environment CR (to remove finalizer)
	mockClient.On("Update", mock.Anything, mock.AnythingOfType("*v1.Environment"), mock.Anything).
		Run(func(args mock.Arguments) {
			updatedEnv := args.Get(1).(*quixiov1.Environment)
			assert.NotContains(t, updatedEnv.Finalizers, EnvironmentFinalizer, "Finalizer should be removed")
		}).
		Return(nil)

	// Create the namespace manager mock
	nsManager := &MockNamespaceManager{}
	nsManager.On("IsNamespaceDeleted", mock.Anything, mock.Anything).Return(true)

	// Create status updater mock
	statusUpdater := &MockStatusUpdater{}

	// Create the reconciler with constructor
	reconciler, err := NewEnvironmentReconciler(
		mockClient,
		s,
		record.NewFakeRecorder(10),
		&config.OperatorConfig{
			NamespaceSuffix: "-suffix",
		},
		nsManager,
		statusUpdater,
	)
	assert.NoError(t, err)

	// Call handleDeletion
	result, err := reconciler.handleDeletion(context.Background(), env)

	// Check the result
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify that all expected method calls were made
	mockClient.AssertExpectations(t)
}

func TestHandleDeletion_NamespaceHasDeletionTimestamp(t *testing.T) {
	// Register the custom resource
	s := scheme.Scheme
	s.AddKnownTypes(quixiov1.GroupVersion, &quixiov1.Environment{})

	// Create a test environment
	env := &quixiov1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-env",
			Namespace: "default",
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

	// Create a namespace with deletion timestamp
	deletionTime := metav1.NewTime(time.Now())
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-suffix",
			DeletionTimestamp: &deletionTime,
			// Add a finalizer to satisfy the validation in fake client
			Finalizers: []string{"kubernetes"},
		},
	}

	// Create a client with the environment and namespace
	client := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(env, namespace).
		WithStatusSubresource(&quixiov1.Environment{}).
		Build()

	// Create the namespace manager mock
	nsManager := &MockNamespaceManager{}
	nsManager.On("IsNamespaceDeleted", mock.Anything, mock.Anything).Return(true)

	// Create status updater mock
	statusUpdater := &MockStatusUpdater{}

	// Create the reconciler with constructor
	reconciler, err := NewEnvironmentReconciler(
		client,
		s,
		record.NewFakeRecorder(10),
		&config.OperatorConfig{
			NamespaceSuffix: "-suffix",
		},
		nsManager,
		statusUpdater,
	)
	assert.NoError(t, err)

	// Call handleDeletion
	result, err := reconciler.handleDeletion(context.Background(), env)

	// Check the result
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify the finalizer was removed
	updatedEnv := &quixiov1.Environment{}
	err = client.Get(context.Background(), types.NamespacedName{
		Name:      env.Name,
		Namespace: env.Namespace,
	}, updatedEnv)
	assert.NoError(t, err)
	assert.NotContains(t, updatedEnv.Finalizers, EnvironmentFinalizer)
}

// =========================================================
// Mock implementations used for testing
// =========================================================

// MockClient implements the Client interface for testing
type MockClient struct {
	mock.Mock
	client.Client
}

func (m *MockClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	args := m.Called(ctx, key, obj)

	// If there's a mock implementation that sets obj, it should have done so
	return args.Error(0)
}

func (m *MockClient) Status() client.StatusWriter {
	args := m.Called()
	return args.Get(0).(client.StatusWriter)
}

func (m *MockClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	args := m.Called(ctx, obj, opts)
	return args.Error(0)
}

func (m *MockClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	args := m.Called(ctx, obj, opts)
	return args.Error(0)
}

// MockStatusWriter implements the StatusWriter interface for testing
type MockStatusWriter struct {
	mock.Mock
}

func (m *MockStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	args := m.Called(ctx, obj)
	return args.Error(0)
}

func (m *MockStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	args := m.Called(ctx, obj, patch)
	return args.Error(0)
}

func (m *MockStatusWriter) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	args := m.Called(ctx, obj, subResource)
	return args.Error(0)
}

// MockStatusUpdater is a mock implementation of status.StatusUpdater
type MockStatusUpdater struct {
	mock.Mock
}

// UpdateStatus implements status.StatusUpdater
func (m *MockStatusUpdater) UpdateStatus(ctx context.Context, env *quixiov1.Environment, updates func(*quixiov1.EnvironmentStatus)) error {
	args := m.Called(ctx, env, updates)

	// Execute the updates function on the provided environment to simulate the update
	if args.Get(0) == nil {
		updates(&env.Status)
	}

	return args.Error(0)
}

// SetSuccessStatus implements status.StatusUpdater
func (m *MockStatusUpdater) SetSuccessStatus(ctx context.Context, env *quixiov1.Environment, conditionType, message string) {
	m.Called(ctx, env, conditionType, message)
}

// SetErrorStatus implements status.StatusUpdater
func (m *MockStatusUpdater) SetErrorStatus(ctx context.Context, env *quixiov1.Environment, phase quixiov1.EnvironmentPhase, conditionType string, err error, eventMsg string) error {
	args := m.Called(ctx, env, phase, conditionType, err, eventMsg)
	return args.Error(0)
}

// MockNamespaceManager is a mock implementation of namespaces.NamespaceManager
type MockNamespaceManager struct {
	mock.Mock
}

// ApplyMetadata implements namespaces.NamespaceManager
func (m *MockNamespaceManager) ApplyMetadata(env *quixiov1.Environment, namespace *corev1.Namespace) bool {
	args := m.Called(env, namespace)
	return args.Bool(0)
}

// UpdateMetadata implements namespaces.NamespaceManager
func (m *MockNamespaceManager) UpdateMetadata(ctx context.Context, env *quixiov1.Environment, namespace *corev1.Namespace) error {
	args := m.Called(ctx, env, namespace)
	return args.Error(0)
}

// IsNamespaceDeleted implements namespaces.NamespaceManager
func (m *MockNamespaceManager) IsNamespaceDeleted(namespace *corev1.Namespace, err error) bool {
	args := m.Called(namespace, err)
	return args.Bool(0)
}
