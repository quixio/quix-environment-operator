package status

import (
	"context"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
)

// MockStatusUpdater implements StatusUpdater for tests
type MockStatusUpdater struct {
	// Functions that can be set by tests to override behavior
	UpdateStatusFunc     func(ctx context.Context, env *quixiov1.Environment, updates func(*quixiov1.EnvironmentStatus)) error
	SetSuccessStatusFunc func(ctx context.Context, env *quixiov1.Environment, message string) error
	SetErrorStatusFunc   func(ctx context.Context, env *quixiov1.Environment, phase quixiov1.EnvironmentPhase, err error, eventMsg string) error

	// Embed default implementation for non-mocked methods
	DefaultUpdater *DefaultStatusUpdater
}

// NewMockStatusUpdater creates a new mock status updater
func NewMockStatusUpdater(defaultUpdater *DefaultStatusUpdater) *MockStatusUpdater {
	return &MockStatusUpdater{
		DefaultUpdater: defaultUpdater,
	}
}

// UpdateStatus uses the mock function if provided, or falls back to default behavior
func (m *MockStatusUpdater) UpdateStatus(ctx context.Context, env *quixiov1.Environment, updates func(*quixiov1.EnvironmentStatus)) error {
	if m.UpdateStatusFunc != nil {
		return m.UpdateStatusFunc(ctx, env, updates)
	}
	if m.DefaultUpdater != nil {
		return m.DefaultUpdater.UpdateStatus(ctx, env, updates)
	}
	// Default implementation if no other behavior is specified
	return nil
}

// SetSuccessStatus uses the mock function if provided, or falls back to default behavior
func (m *MockStatusUpdater) SetSuccessStatus(ctx context.Context, env *quixiov1.Environment, message string) error {
	if m.SetSuccessStatusFunc != nil {
		return m.SetSuccessStatusFunc(ctx, env, message)
	}
	if m.DefaultUpdater != nil {
		return m.DefaultUpdater.SetSuccessStatus(ctx, env, message)
	}
	return nil
}

// SetErrorStatus uses the mock function if provided, or falls back to default behavior
func (m *MockStatusUpdater) SetErrorStatus(ctx context.Context, env *quixiov1.Environment, phase quixiov1.EnvironmentPhase, err error, eventMsg string) error {
	if m.SetErrorStatusFunc != nil {
		return m.SetErrorStatusFunc(ctx, env, phase, err, eventMsg)
	}
	if m.DefaultUpdater != nil {
		return m.DefaultUpdater.SetErrorStatus(ctx, env, phase, err, eventMsg)
	}
	// Return original error even if no default updater
	return err
}
