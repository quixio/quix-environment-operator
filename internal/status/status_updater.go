package status

import (
	"context"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
)

// StatusUpdater defines operations for updating Environment status
type StatusUpdater interface {
	// UpdateStatus updates the Environment status with retry mechanism
	UpdateStatus(ctx context.Context, env *quixiov1.Environment, updates func(*quixiov1.EnvironmentStatus)) error

	// SetSuccessStatus sets the Ready condition to True, Phase to Ready, and clears ErrorMessage.
	SetSuccessStatus(ctx context.Context, env *quixiov1.Environment, message string) error

	// SetErrorStatus sets the Ready condition to False, sets the Phase, and sets ErrorMessage.
	SetErrorStatus(ctx context.Context, env *quixiov1.Environment, phase quixiov1.EnvironmentPhase, err error, eventMsg string) error
}
