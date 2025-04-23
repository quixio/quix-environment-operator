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
)

func TestValidateEnvironment(t *testing.T) {
	// Register the custom resource
	s := scheme.Scheme
	s.AddKnownTypes(quixiov1.GroupVersion, &quixiov1.Environment{})

	// Create a mock client
	client := fake.NewClientBuilder().WithScheme(s).Build()

	// Create the reconciler with a mock recorder
	recorder := record.NewFakeRecorder(10)
	reconciler := &EnvironmentReconciler{
		Client:   client,
		Scheme:   s,
		Recorder: recorder,
		Config: &config.OperatorConfig{
			NamespaceSuffix:         "-suffix",
			ServiceAccountName:      "test-sa",
			ServiceAccountNamespace: "test-ns",
			ClusterRoleName:         "test-role",
		},
	}

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

	// Create the reconciler with a mock recorder
	recorder := record.NewFakeRecorder(10)
	reconciler := &EnvironmentReconciler{
		Client:   client,
		Scheme:   s,
		Recorder: recorder,
		Config: &config.OperatorConfig{
			NamespaceSuffix:         "-suffix",
			ServiceAccountName:      "test-sa",
			ServiceAccountNamespace: "test-ns",
			ClusterRoleName:         "test-role",
		},
	}

	// Test adding a condition
	condition := metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "Testing",
		Message: "This is a test condition",
	}

	reconciler.setCondition(context.Background(), env, condition)

	// Verify the condition was set
	updatedEnv := &quixiov1.Environment{}
	err := client.Get(context.Background(), types.NamespacedName{
		Name:      env.Name,
		Namespace: env.Namespace,
	}, updatedEnv)
	assert.NoError(t, err)

	// Check if the condition was added
	assert.Len(t, updatedEnv.Status.Conditions, 1)
	assert.Equal(t, "Ready", updatedEnv.Status.Conditions[0].Type)
	assert.Equal(t, metav1.ConditionTrue, updatedEnv.Status.Conditions[0].Status)
	assert.Equal(t, "Testing", updatedEnv.Status.Conditions[0].Reason)
	assert.Equal(t, "This is a test condition", updatedEnv.Status.Conditions[0].Message)
	assert.Equal(t, int64(1), updatedEnv.Status.Conditions[0].ObservedGeneration)
	assert.False(t, updatedEnv.Status.Conditions[0].LastTransitionTime.IsZero())

	// Test updating a condition
	updatedCondition := metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  "TestingUpdate",
		Message: "Updated condition",
	}

	reconciler.setCondition(context.Background(), updatedEnv, updatedCondition)

	// Verify the condition was updated
	finalEnv := &quixiov1.Environment{}
	err = client.Get(context.Background(), types.NamespacedName{
		Name:      env.Name,
		Namespace: env.Namespace,
	}, finalEnv)
	assert.NoError(t, err)

	// Check if the condition was updated
	assert.Len(t, finalEnv.Status.Conditions, 1)
	assert.Equal(t, "Ready", finalEnv.Status.Conditions[0].Type)
	assert.Equal(t, metav1.ConditionFalse, finalEnv.Status.Conditions[0].Status)
	assert.Equal(t, "TestingUpdate", finalEnv.Status.Conditions[0].Reason)
	assert.Equal(t, "Updated condition", finalEnv.Status.Conditions[0].Message)
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
		setupMock     func(*MockClient, *MockStatusWriter)
		updateFn      func(*quixiov1.EnvironmentStatus)
		expectedError bool
	}{
		{
			name: "Successful status update",
			setupMock: func(mockClient *MockClient, mockStatusWriter *MockStatusWriter) {
				mockClient.On("Get", mock.Anything, types.NamespacedName{Name: "test-env", Namespace: "default"}, mock.AnythingOfType("*v1.Environment")).
					Run(func(args mock.Arguments) {
						// Copy the environment to the argument
						envArg := args.Get(2).(*quixiov1.Environment)
						*envArg = *env
					}).
					Return(nil)
				mockClient.On("Status").Return(mockStatusWriter)
				mockStatusWriter.On("Update", mock.Anything, mock.AnythingOfType("*v1.Environment")).Return(nil)
			},
			updateFn: func(status *quixiov1.EnvironmentStatus) {
				status.Phase = quixiov1.PhaseReady
				status.Namespace = "test-namespace"
			},
			expectedError: false,
		},
		{
			name: "Failed to get environment",
			setupMock: func(mockClient *MockClient, mockStatusWriter *MockStatusWriter) {
				mockClient.On("Get", mock.Anything, types.NamespacedName{Name: "test-env", Namespace: "default"}, mock.AnythingOfType("*v1.Environment")).
					Return(errors.New("get error"))
			},
			updateFn: func(status *quixiov1.EnvironmentStatus) {
				status.Phase = quixiov1.PhaseReady
			},
			expectedError: true,
		},
		{
			name: "Status update conflict then success",
			setupMock: func(mockClient *MockClient, mockStatusWriter *MockStatusWriter) {
				// First call returns the environment
				mockClient.On("Get", mock.Anything, types.NamespacedName{Name: "test-env", Namespace: "default"}, mock.AnythingOfType("*v1.Environment")).
					Run(func(args mock.Arguments) {
						// Copy the environment to the argument
						envArg := args.Get(2).(*quixiov1.Environment)
						*envArg = *env
					}).
					Return(nil).Once()

				mockClient.On("Status").Return(mockStatusWriter)

				// First update fails with conflict
				mockStatusWriter.On("Update", mock.Anything, mock.AnythingOfType("*v1.Environment")).
					Return(k8serrors.NewConflict(schema.GroupResource{Group: "quix.io", Resource: "environments"}, "test-env", errors.New("conflict"))).Once()

				// Second get call
				mockClient.On("Get", mock.Anything, types.NamespacedName{Name: "test-env", Namespace: "default"}, mock.AnythingOfType("*v1.Environment")).
					Run(func(args mock.Arguments) {
						// Copy the environment to the argument
						envArg := args.Get(2).(*quixiov1.Environment)
						*envArg = *env
					}).
					Return(nil).Once()

				// Second update succeeds
				mockStatusWriter.On("Update", mock.Anything, mock.AnythingOfType("*v1.Environment")).
					Return(nil).Once()
			},
			updateFn: func(status *quixiov1.EnvironmentStatus) {
				status.Phase = quixiov1.PhaseReady
			},
			expectedError: false,
		},
		{
			name: "Non-conflict error in status update",
			setupMock: func(mockClient *MockClient, mockStatusWriter *MockStatusWriter) {
				mockClient.On("Get", mock.Anything, types.NamespacedName{Name: "test-env", Namespace: "default"}, mock.AnythingOfType("*v1.Environment")).
					Run(func(args mock.Arguments) {
						// Copy the environment to the argument
						envArg := args.Get(2).(*quixiov1.Environment)
						*envArg = *env
					}).
					Return(nil)
				mockClient.On("Status").Return(mockStatusWriter)
				mockStatusWriter.On("Update", mock.Anything, mock.AnythingOfType("*v1.Environment")).
					Return(errors.New("non-conflict error"))
			},
			updateFn: func(status *quixiov1.EnvironmentStatus) {
				status.Phase = quixiov1.PhaseReady
			},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockClient{}
			mockStatusWriter := &MockStatusWriter{}

			tt.setupMock(mockClient, mockStatusWriter)

			reconciler := &EnvironmentReconciler{
				Client: mockClient,
				Config: &config.OperatorConfig{},
			}

			err := reconciler.updateEnvironmentStatus(context.Background(), env, tt.updateFn)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockClient.AssertExpectations(t)
			mockStatusWriter.AssertExpectations(t)
		})
	}
}

func TestVerifyNamespaceAnnotations(t *testing.T) {
	// Register the custom resource
	s := scheme.Scheme
	s.AddKnownTypes(quixiov1.GroupVersion, &quixiov1.Environment{})

	tests := []struct {
		name          string
		annotations   map[string]string
		expectedError bool
	}{
		{
			name: "All required annotations present",
			annotations: map[string]string{
				"quix.io/environment-crd-namespace": "default",
				"quix.io/environment-id":            "test-id",
				"quix.io/environment-resource-name": "test-env",
				"quix.io/created-by":                "quix-environment-operator",
			},
			expectedError: false,
		},
		{
			name: "Missing environment-crd-namespace annotation",
			annotations: map[string]string{
				"quix.io/environment-id":            "test-id",
				"quix.io/environment-resource-name": "test-env",
			},
			expectedError: true,
		},
		{
			name: "Missing environment-id annotation",
			annotations: map[string]string{
				"quix.io/environment-crd-namespace": "default",
				"quix.io/environment-resource-name": "test-env",
			},
			expectedError: true,
		},
		{
			name: "Missing environment-resource-name annotation",
			annotations: map[string]string{
				"quix.io/environment-crd-namespace": "default",
				"quix.io/environment-id":            "test-id",
			},
			expectedError: true,
		},
		{
			name:          "No annotations",
			annotations:   map[string]string{},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create namespace with test annotations
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-namespace",
					Annotations: tt.annotations,
				},
			}

			// Create client with namespace
			client := fake.NewClientBuilder().
				WithScheme(s).
				WithObjects(namespace).
				Build()

			// Create recorder that captures events
			recorder := record.NewFakeRecorder(10)

			reconciler := &EnvironmentReconciler{
				Client:   client,
				Scheme:   s,
				Recorder: recorder,
				Config:   &config.OperatorConfig{},
			}

			// Call the function
			err := reconciler.verifyNamespaceAnnotations(context.Background(), "test-namespace")

			if tt.expectedError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "missing required annotations")
			} else {
				assert.NoError(t, err)
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

			// Create the test environment
			env := &quixiov1.Environment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-env",
					Namespace: "default",
				},
				Spec: tt.environmentSpec,
			}

			// Create the reconciler
			reconciler := &EnvironmentReconciler{
				Client: mockClient,
				Config: &config.OperatorConfig{
					NamespaceSuffix: "-suffix",
				},
			}

			// Call createNamespace
			err := reconciler.createNamespace(context.Background(), env, "test-suffix")

			// Check the result
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Verify that all expected method calls were made
			mockClient.AssertExpectations(t)
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

	// Create the reconciler
	reconciler := &EnvironmentReconciler{
		Client: mockClient,
		Config: &config.OperatorConfig{
			NamespaceSuffix: "-suffix",
		},
		Recorder: record.NewFakeRecorder(10),
	}

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

	// Create the reconciler
	reconciler := &EnvironmentReconciler{
		Client: client,
		Config: &config.OperatorConfig{
			NamespaceSuffix: "-suffix",
		},
		Recorder: record.NewFakeRecorder(10),
	}

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
