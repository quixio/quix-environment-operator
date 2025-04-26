package status

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
)

func TestUpdateStatus(t *testing.T) {
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
			Phase: quixiov1.PhaseCreating,
		},
	}

	// Create fake client and add the environment
	scheme := testScheme()
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&quixiov1.Environment{}).
		WithObjects(env).
		Build()

	// Create recorder
	recorder := record.NewFakeRecorder(10)

	// Create status updater
	updater := NewStatusUpdater(client, recorder)

	// Test normal update
	ctx := context.Background()
	err := updater.UpdateStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
		status.Phase = quixiov1.PhaseReady
		status.Namespace = "test-namespace"
	})
	require.NoError(t, err)

	// Verify the update
	updated := &quixiov1.Environment{}
	err = client.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, updated)
	require.NoError(t, err)
	assert.Equal(t, quixiov1.PhaseReady, updated.Status.Phase)
	assert.Equal(t, "test-namespace", updated.Status.Namespace)

	// Test update with client error (using a mock client)
	mockClient := &mockStatusClient{
		Client:    client,
		statusErr: errors.New("status update error"),
	}
	updaterWithError := NewStatusUpdater(mockClient, recorder)

	err = updaterWithError.UpdateStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
		status.Phase = quixiov1.PhaseUpdateFailed
	})
	assert.Error(t, err)
}

func TestSetSuccessStatus(t *testing.T) {
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
			Phase:        quixiov1.PhaseCreating,
			ErrorMessage: "previous error",
			Conditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionFalse,
					LastTransitionTime: metav1.Now(),
					Reason:             "Creating",
					Message:            "Environment is being created",
				},
			},
		},
	}

	// Create fake client
	scheme := testScheme()
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&quixiov1.Environment{}).
		WithObjects(env).
		Build()

	// Create recorder
	recorder := record.NewFakeRecorder(10)

	// Create status updater
	updater := NewStatusUpdater(client, recorder)

	// Test success status update
	ctx := context.Background()
	err := updater.SetSuccessStatus(ctx, env, "Environment provisioned successfully")
	require.NoError(t, err)

	// Verify the update
	updated := &quixiov1.Environment{}
	err = client.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, updated)
	require.NoError(t, err)

	// Check that status is changed
	assert.Equal(t, quixiov1.PhaseReady, updated.Status.Phase)
	assert.Empty(t, updated.Status.ErrorMessage)

	// Check that condition is updated
	readyCondition := findCondition(updated.Status.Conditions, "Ready")
	require.NotNil(t, readyCondition)
	assert.Equal(t, metav1.ConditionTrue, readyCondition.Status)
	assert.Equal(t, "Environment provisioned successfully", readyCondition.Message)
	assert.Equal(t, "Succeeded", readyCondition.Reason)

	// Check recorder got an event
	event := <-recorder.Events
	assert.Contains(t, event, "Succeeded")
}

func TestSetErrorStatus(t *testing.T) {
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
			Phase: quixiov1.PhaseReady,
			Conditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
					Reason:             "Succeeded",
					Message:            "Environment is ready",
				},
			},
		},
	}

	// Create fake client
	scheme := testScheme()
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&quixiov1.Environment{}).
		WithObjects(env).
		Build()

	// Create recorder
	recorder := record.NewFakeRecorder(10)

	// Create status updater
	updater := NewStatusUpdater(client, recorder)

	// Test error status update
	ctx := context.Background()
	testError := errors.New("test error")

	// Note: SetErrorStatus returns the original error by design,
	// so we expect the original error to be returned
	err := updater.SetErrorStatus(ctx, env, quixiov1.PhaseCreateFailed, testError, "Failed to create environment")
	require.Equal(t, testError, err)

	// Verify the update
	updated := &quixiov1.Environment{}
	err = client.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, updated)
	require.NoError(t, err)

	// Check that status is changed
	assert.Equal(t, quixiov1.PhaseCreateFailed, updated.Status.Phase)
	assert.Contains(t, updated.Status.ErrorMessage, "Failed to create environment")
	assert.Contains(t, updated.Status.ErrorMessage, "test error")

	// Check that condition is updated
	readyCondition := findCondition(updated.Status.Conditions, "Ready")
	require.NotNil(t, readyCondition)
	assert.Equal(t, metav1.ConditionFalse, readyCondition.Status)
	assert.Contains(t, readyCondition.Message, "Failed to create environment")
	assert.Equal(t, "Failed", readyCondition.Reason)

	// Check recorder got an event
	event := <-recorder.Events
	assert.Contains(t, event, "Failed to create environment")
}

func TestRetryOnConflict(t *testing.T) {
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
			Phase: quixiov1.PhaseCreating,
		},
	}

	// Create fake client
	scheme := testScheme()
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&quixiov1.Environment{}).
		WithObjects(env).
		Build()

	// Create mock client that returns conflict on first attempts
	retryClient := &retryClient{
		Client:      client,
		conflictErr: k8sConflictError(),
		retries:     2, // Will return conflict twice before succeeding
	}

	// Create recorder
	recorder := record.NewFakeRecorder(10)

	// Create custom updater for testing retries
	updater := &DefaultStatusUpdater{
		Client:   retryClient,
		Recorder: recorder,
	}

	// Test update with retry
	ctx := context.Background()
	err := updater.UpdateStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
		status.Phase = quixiov1.PhaseReady
	})
	require.NoError(t, err)

	// Verify the client retried and succeeded
	assert.Equal(t, 0, retryClient.retries)

	// Verify the update was eventually applied
	updated := &quixiov1.Environment{}
	err = client.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, updated)
	require.NoError(t, err)
	assert.Equal(t, quixiov1.PhaseReady, updated.Status.Phase)
}

// Helper function to find a condition by type
func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i, condition := range conditions {
		if condition.Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// Setup test scheme with required types
func testScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = quixiov1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	return scheme
}

// Mock client for error testing
type mockStatusClient struct {
	client.Client
	statusErr error
}

func (c *mockStatusClient) Status() client.StatusWriter {
	return &mockStatusWriter{
		StatusWriter: c.Client.Status(),
		err:          c.statusErr,
	}
}

type mockStatusWriter struct {
	client.StatusWriter
	err error
}

func (w *mockStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if w.err != nil {
		return w.err
	}
	return w.StatusWriter.Update(ctx, obj, opts...)
}

// Retry client that returns conflict errors for a specified number of retries
type retryClient struct {
	client.Client
	conflictErr error
	retries     int
}

func (c *retryClient) Status() client.StatusWriter {
	return &retryStatusWriter{
		StatusWriter: c.Client.Status(),
		conflictErr:  c.conflictErr,
		retries:      &c.retries,
	}
}

type retryStatusWriter struct {
	client.StatusWriter
	conflictErr error
	retries     *int
}

func (w *retryStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if *w.retries > 0 {
		*w.retries--
		return w.conflictErr
	}
	return w.StatusWriter.Update(ctx, obj, opts...)
}

// Create a k8s conflict error for testing
func k8sConflictError() error {
	return &k8sStatusError{
		status: metav1.Status{
			Status:  "Failure",
			Message: "conflict",
			Reason:  metav1.StatusReasonConflict,
			Code:    409,
		},
	}
}

// Simplified k8s status error for testing
type k8sStatusError struct {
	status metav1.Status
}

func (e *k8sStatusError) Error() string {
	return e.status.Message
}

func (e *k8sStatusError) Status() metav1.Status {
	return e.status
}
