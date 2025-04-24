package status

import (
	"context"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
)

// MockStatusUpdater is a mock implementation of StatusUpdater for testing.
type MockStatusUpdater struct {
	UpdateStatusFunc     func(ctx context.Context, env *quixiov1.Environment, updates func(*quixiov1.EnvironmentStatus)) error
	SetSuccessStatusFunc func(ctx context.Context, env *quixiov1.Environment, conditionType, message string)
	SetErrorStatusFunc   func(ctx context.Context, env *quixiov1.Environment, phase quixiov1.EnvironmentPhase, conditionType string, err error, eventMsg string) error
}

// UpdateStatus calls UpdateStatusFunc if set, otherwise returns nil.
func (m *MockStatusUpdater) UpdateStatus(ctx context.Context, env *quixiov1.Environment, updates func(*quixiov1.EnvironmentStatus)) error {
	if m.UpdateStatusFunc != nil {
		return m.UpdateStatusFunc(ctx, env, updates)
	}
	// Default behavior: apply updates and return nil
	updates(&env.Status)
	return nil
}

// SetSuccessStatus calls SetSuccessStatusFunc if set, otherwise does nothing.
func (m *MockStatusUpdater) SetSuccessStatus(ctx context.Context, env *quixiov1.Environment, conditionType, message string) {
	if m.SetSuccessStatusFunc != nil {
		m.SetSuccessStatusFunc(ctx, env, conditionType, message)
	}
}

// SetErrorStatus calls SetErrorStatusFunc if set, otherwise returns nil.
func (m *MockStatusUpdater) SetErrorStatus(ctx context.Context, env *quixiov1.Environment, phase quixiov1.EnvironmentPhase, conditionType string, err error, eventMsg string) error {
	if m.SetErrorStatusFunc != nil {
		return m.SetErrorStatusFunc(ctx, env, phase, conditionType, err, eventMsg)
	}
	return nil
}
