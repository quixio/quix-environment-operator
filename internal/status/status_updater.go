package status

import (
	"context"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
)

// StatusUpdater defines operations for updating Environment status
type StatusUpdater interface {
	// UpdateStatus updates the Environment status with retry mechanism
	UpdateStatus(ctx context.Context, env *quixiov1.Environment, updates func(*quixiov1.EnvironmentStatus)) error

	// SetSuccessStatus sets a condition to Success state
	SetSuccessStatus(ctx context.Context, env *quixiov1.Environment, conditionType, message string)

	// SetErrorStatus sets error conditions and updates status
	SetErrorStatus(ctx context.Context, env *quixiov1.Environment, phase quixiov1.EnvironmentPhase, conditionType string, err error, eventMsg string) error
}
