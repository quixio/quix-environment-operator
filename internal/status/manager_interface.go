package status

import (
	"context"

	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
)

// StatusUpdater defines operations for updating environment status
type StatusUpdater interface {
	// UpdateStatus applies changes to the Environment status
	UpdateStatus(ctx context.Context, env *v1.Environment, updater func(*v1.EnvironmentStatus)) error

	// SetErrorStatus sets error information in the status
	SetErrorStatus(ctx context.Context, env *v1.Environment, phase v1.EnvironmentPhase, err error, message string) error

	// SetReadyCondition sets the Ready condition based on the state
	SetReadyCondition(ctx context.Context, env *v1.Environment, isReady bool, reason, message string) error

	// UpdateSubResourcePhase updates the phase of a sub-resource (namespace, rolebinding)
	UpdateSubResourcePhase(ctx context.Context, env *v1.Environment, resourceType string, phase string) error
}
