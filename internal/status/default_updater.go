package status

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
)

// Condition constants
const (
	// Condition types
	ConditionTypeReady              = "Ready"
	ConditionTypeNamespaceCreated   = "NamespaceCreated"
	ConditionTypeRoleBindingCreated = "RoleBindingCreated"
	ConditionTypeNamespaceDeleted   = "NamespaceDeleted"

	// Condition reasons
	ReasonSucceeded       = "Succeeded"
	ReasonInProgress      = "InProgress"
	ReasonFailed          = "Failed"
	ReasonValidationError = "ValidationError"
)

// DefaultStatusUpdater is the standard implementation of StatusUpdater
type DefaultStatusUpdater struct {
	Client   client.Client
	Recorder record.EventRecorder
}

// NewStatusUpdater creates a new DefaultStatusUpdater
func NewStatusUpdater(client client.Client, recorder record.EventRecorder) *DefaultStatusUpdater {
	return &DefaultStatusUpdater{
		Client:   client,
		Recorder: recorder,
	}
}

// UpdateStatus updates the Environment status with retry mechanism for handling conflicts
func (u *DefaultStatusUpdater) UpdateStatus(ctx context.Context, env *quixiov1.Environment,
	updates func(*quixiov1.EnvironmentStatus)) error {

	logger := log.FromContext(ctx)

	// Define retry parameters
	maxRetries := 5
	retryDelay := 100 * time.Millisecond

	var lastErr error

	// Retry loop with exponential backoff
	for attempt := 0; attempt < maxRetries; attempt++ {
		// If this is a retry, wait with exponential backoff
		if attempt > 0 {
			backoffTime := retryDelay * time.Duration(1<<uint(attempt-1))
			logger.Info("Retrying status update after conflict",
				"attempt", attempt+1,
				"maxRetries", maxRetries,
				"backoffTime", backoffTime)
			time.Sleep(backoffTime)
		}

		// Get the latest environment object
		latestEnv := &quixiov1.Environment{}
		if err := u.Client.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, latestEnv); err != nil {
			lastErr = err
			logger.Error(err, "Failed to get latest Environment for status update")
			continue
		}

		// Apply the updates to the status
		updates(&latestEnv.Status)

		// Update the status with the latest version
		if err := u.Client.Status().Update(ctx, latestEnv); err != nil {
			lastErr = err

			// Only retry if it's a conflict error
			if !errors.IsConflict(err) {
				logger.Error(err, "Failed to update Environment status (non-conflict error)")
				return err
			}

			logger.Info("Conflict detected while updating Environment status, will retry",
				"attempt", attempt+1,
				"maxRetries", maxRetries)
			continue
		}

		// Success
		return nil
	}

	// If we get here, we've exhausted our retries
	logger.Error(lastErr, "Failed to update Environment status after max retries",
		"maxRetries", maxRetries)
	return lastErr
}

// SetSuccessStatus sets a condition to Success state with standardized parameters
func (u *DefaultStatusUpdater) SetSuccessStatus(ctx context.Context, env *quixiov1.Environment,
	conditionType, message string) {

	// Set the condition
	u.setCondition(ctx, env, metav1.Condition{
		Type:    conditionType,
		Status:  metav1.ConditionTrue,
		Reason:  ReasonSucceeded,
		Message: message,
	})

	// Optionally record an event with the same message (when it's significant)
	if conditionType == ConditionTypeReady || conditionType == ConditionTypeNamespaceCreated ||
		conditionType == ConditionTypeRoleBindingCreated {
		eventName := strings.ReplaceAll(conditionType, " ", "")
		u.Recorder.Eventf(env, corev1.EventTypeNormal, eventName, "%s", message)
	}
}

// SetErrorStatus updates status and conditions when an error occurs during reconciliation
func (u *DefaultStatusUpdater) SetErrorStatus(ctx context.Context, env *quixiov1.Environment,
	phase quixiov1.EnvironmentPhase, conditionType string, err error, eventMsg string) error {

	logger := log.FromContext(ctx)
	errMsg := fmt.Sprintf("%s: %v", eventMsg, err)

	// Log the error
	logger.Error(err, eventMsg)

	// Record event
	u.Recorder.Eventf(env, corev1.EventTypeWarning, strings.ReplaceAll(conditionType, " ", ""), "%s", errMsg)

	// Update status with error
	statusErr := u.UpdateStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
		status.Phase = phase
		status.ErrorMessage = errMsg
	})
	if statusErr != nil {
		logger.Error(statusErr, "Failed to update status with error message")
	}

	// Set condition
	u.setCondition(ctx, env, metav1.Condition{
		Type:    conditionType,
		Status:  metav1.ConditionFalse,
		Reason:  ReasonFailed,
		Message: errMsg,
	})

	// Return the original error
	return err
}

// setCondition updates a condition in the Environment resource status
func (u *DefaultStatusUpdater) setCondition(ctx context.Context, env *quixiov1.Environment, condition metav1.Condition) {
	logger := log.FromContext(ctx)

	// Define retry parameters
	maxRetries := 5
	retryDelay := 100 * time.Millisecond

	var lastErr error

	// Retry loop with exponential backoff
	for attempt := 0; attempt < maxRetries; attempt++ {
		// If this is a retry, wait with exponential backoff
		if attempt > 0 {
			backoffTime := retryDelay * time.Duration(1<<uint(attempt-1))
			logger.Info("Retrying condition update after conflict",
				"attempt", attempt+1,
				"maxRetries", maxRetries,
				"backoffTime", backoffTime)
			time.Sleep(backoffTime)
		}

		// Get the latest Environment object to avoid conflicts
		latestEnv := &quixiov1.Environment{}
		if err := u.Client.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, latestEnv); err != nil {
			lastErr = err
			logger.Error(err, "Failed to get latest Environment for status condition update")
			continue
		}

		// Set the last transition time if not already set
		if condition.LastTransitionTime.IsZero() {
			condition.LastTransitionTime = metav1.NewTime(time.Now())
		}

		// Set the observedGeneration to the resource's generation
		condition.ObservedGeneration = latestEnv.Generation

		// Find and update the condition
		meta.SetStatusCondition(&latestEnv.Status.Conditions, condition)

		// Update the status using the latest version
		if err := u.Client.Status().Update(ctx, latestEnv); err != nil {
			lastErr = err

			// Only retry if it's a conflict error
			if !errors.IsConflict(err) {
				logger.Error(err, "Failed to update Environment status condition (non-conflict error)")
				return
			}

			logger.Info("Conflict detected while updating Environment condition, will retry",
				"attempt", attempt+1,
				"maxRetries", maxRetries)
			continue
		}

		// Success
		return
	}

	// If we get here, we've exhausted our retries
	logger.Error(lastErr, "Failed to update Environment status condition after max retries",
		"condition", condition.Type,
		"maxRetries", maxRetries)
}
