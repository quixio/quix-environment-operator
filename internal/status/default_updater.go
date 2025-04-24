package status

import (
	"context"
	"fmt"
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
	ReasonSucceeded            = "Succeeded"
	ReasonInProgress           = "InProgress"
	ReasonFailed               = "Failed"
	ReasonValidationError      = "ValidationError"
	ReasonNamespaceTerminating = "NamespaceTerminating"
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

		// Capture current generation before applying updates
		currentGeneration := latestEnv.Generation

		// Apply the updates to the status
		updates(&latestEnv.Status)

		// Set ObservedGeneration for all conditions after updates are applied
		for i := range latestEnv.Status.Conditions {
			latestEnv.Status.Conditions[i].ObservedGeneration = currentGeneration
			// Set LastTransitionTime if it's a new condition or status changed
			currentCondition := latestEnv.Status.Conditions[i]
			prevCondition := meta.FindStatusCondition(env.Status.Conditions, currentCondition.Type)
			if prevCondition == nil || prevCondition.Status != currentCondition.Status {
				latestEnv.Status.Conditions[i].LastTransitionTime = metav1.NewTime(time.Now())
			}
		}

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

// SetSuccessStatus sets the Ready condition to True, Phase to Ready, and clears ErrorMessage.
func (u *DefaultStatusUpdater) SetSuccessStatus(ctx context.Context, env *quixiov1.Environment,
	message string) error {

	logger := log.FromContext(ctx)
	statusErr := u.UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
		st.Phase = quixiov1.PhaseReady
		st.ErrorMessage = ""
		meta.SetStatusCondition(&st.Conditions, metav1.Condition{
			Type:    ConditionTypeReady,
			Status:  metav1.ConditionTrue,
			Reason:  ReasonSucceeded,
			Message: message,
		})
	})

	if statusErr != nil {
		logger.Error(statusErr, "Failed to set success status")
		return statusErr
	}

	u.Recorder.Eventf(env, corev1.EventTypeNormal, ReasonSucceeded, "%s", message)
	return nil
}

// SetErrorStatus sets the Ready condition to False, sets the Phase, and sets ErrorMessage.
func (u *DefaultStatusUpdater) SetErrorStatus(ctx context.Context, env *quixiov1.Environment,
	phase quixiov1.EnvironmentPhase, err error, eventMsg string) error {

	logger := log.FromContext(ctx)
	errMsg := fmt.Sprintf("%s: %v", eventMsg, err)

	logger.Error(err, eventMsg) // Log the original error
	u.Recorder.Eventf(env, corev1.EventTypeWarning, ReasonFailed, "%s", errMsg)

	statusErr := u.UpdateStatus(ctx, env, func(st *quixiov1.EnvironmentStatus) {
		st.Phase = phase
		st.ErrorMessage = errMsg
		meta.SetStatusCondition(&st.Conditions, metav1.Condition{
			Type:    ConditionTypeReady,
			Status:  metav1.ConditionFalse,
			Reason:  ReasonFailed,
			Message: errMsg,
		})
	})

	if statusErr != nil {
		// Log the status update error, but return the original reconciling error
		logger.Error(statusErr, "Failed to update status with error message")
	}

	// Return the original error that triggered this status update
	return err
}
