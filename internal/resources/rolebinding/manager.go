package rolebinding

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
	"github.com/quix-analytics/quix-environment-operator/internal/resources/namespace"
	"github.com/quix-analytics/quix-environment-operator/internal/security"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DefaultManager implements the Manager interface
type DefaultManager struct {
	client            client.Client
	config            config.ConfigProvider
	logger            logr.Logger
	securityValidator *security.Validator
	namespaceManager  namespace.Manager
}

// NewManager creates a new role binding manager
func NewManager(client client.Client, config config.ConfigProvider, logger logr.Logger, namespaceManager namespace.Manager) *DefaultManager {
	return &DefaultManager{
		client:            client,
		config:            config,
		logger:            logger.WithName("rolebinding-manager"),
		securityValidator: security.NewValidator(client),
		namespaceManager:  namespaceManager,
	}
}

// GetName returns the name of the role binding for the environment
func (m *DefaultManager) GetName(env *v1.Environment) string {
	return fmt.Sprintf("%s-quix-crb", env.Spec.Id)
}

// Exists checks if a role binding exists
func (m *DefaultManager) Exists(ctx context.Context, env *v1.Environment) (bool, error) {
	// Role binding name
	rbName := m.GetName(env)
	namespace := m.namespaceManager.GetNamespaceName(env)

	// Check if role binding exists
	rb := &rbacv1.RoleBinding{}
	err := m.client.Get(ctx, types.NamespacedName{
		Name:      rbName,
		Namespace: namespace,
	}, rb)

	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// Delete deletes a role binding
func (m *DefaultManager) Delete(ctx context.Context, env *v1.Environment) error {
	// Get namespace name
	namespace := m.namespaceManager.GetNamespaceName(env)

	// Role binding name
	rbName := m.GetName(env)

	m.logger.V(0).Info("Deleting role binding", "name", rbName, "namespace", namespace)

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbName,
			Namespace: namespace,
		},
	}

	err := m.client.Delete(ctx, rb)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete role binding: %w", err)
	}

	return nil
}

// Reconcile creates or updates a role binding
func (m *DefaultManager) Reconcile(ctx context.Context, env *v1.Environment) (*rbacv1.RoleBinding, error) {
	// Get namespace name
	namespace := m.namespaceManager.GetNamespaceName(env)

	m.logger.V(1).Info("Reconciling role binding", "namespace", namespace)

	// Validate the ClusterRole for security purposes
	if err := m.securityValidator.ValidateClusterRole(ctx, m.config.GetClusterRoleName()); err != nil {
		m.logger.Error(err, "Security validation failed for ClusterRole", "clusterRole", m.config.GetClusterRoleName())
		return nil, err
	}

	rbName := m.GetName(env)

	roleBinding := &rbacv1.RoleBinding{}
	err := m.client.Get(ctx, types.NamespacedName{Name: rbName, Namespace: namespace}, roleBinding)

	if err != nil {
		if !errors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to check if role binding exists: %w", err)
		}

		roleBinding = &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      rbName,
				Namespace: namespace,
				Labels: map[string]string{
					"quix.io/environment-id":   env.Spec.Id,
					"quix.io/managed-by":       "environment-operator",
					"quix.io/environment-name": env.Name,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     m.config.GetClusterRoleName(),
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      m.config.GetServiceAccountName(),
					Namespace: m.config.GetServiceAccountNamespace(),
				},
			},
		}

		m.logger.V(0).Info("Creating role binding", "name", rbName, "namespace", namespace)
		err = m.client.Create(ctx, roleBinding)
		if err != nil {
			return nil, fmt.Errorf("failed to create role binding: %w", err)
		}
	} else {
		updated := false

		if roleBinding.Labels == nil {
			roleBinding.Labels = make(map[string]string)
		}

		labelMap := map[string]string{
			"quix.io/environment-id":   env.Spec.Id,
			"quix.io/managed-by":       "environment-operator",
			"quix.io/environment-name": env.Name,
		}

		for k, v := range labelMap {
			if roleBinding.Labels[k] != v {
				roleBinding.Labels[k] = v
				updated = true
			}
		}

		expectedSubject := rbacv1.Subject{
			Kind:      "ServiceAccount",
			Name:      m.config.GetServiceAccountName(),
			Namespace: m.config.GetServiceAccountNamespace(),
		}

		if len(roleBinding.Subjects) == 0 {
			roleBinding.Subjects = []rbacv1.Subject{expectedSubject}
			updated = true
		} else {
			hasExpectedServiceAccount := false
			for _, subject := range roleBinding.Subjects {
				if subject.Kind == "ServiceAccount" {
					if subject.Name == expectedSubject.Name &&
						subject.Namespace == expectedSubject.Namespace {
						hasExpectedServiceAccount = true
					}
				}
			}

			if !hasExpectedServiceAccount {
				roleBinding.Subjects = []rbacv1.Subject{expectedSubject}
				updated = true
			}
		}

		expectedRoleRef := rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     m.config.GetClusterRoleName(),
		}

		if roleBinding.RoleRef.Name != expectedRoleRef.Name ||
			roleBinding.RoleRef.Kind != expectedRoleRef.Kind ||
			roleBinding.RoleRef.APIGroup != expectedRoleRef.APIGroup {

			if err := m.client.Delete(ctx, roleBinding); err != nil {
				return nil, fmt.Errorf("failed to delete role binding for update: %w", err)
			}

			roleBinding = &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbName,
					Namespace: namespace,
					Labels:    labelMap,
				},
				RoleRef:  expectedRoleRef,
				Subjects: []rbacv1.Subject{expectedSubject},
			}

			m.logger.V(0).Info("Recreating role binding with updated RoleRef", "name", rbName, "namespace", namespace)
			if err := m.client.Create(ctx, roleBinding); err != nil {
				return nil, fmt.Errorf("failed to recreate role binding: %w", err)
			}

			updated = false // We've already created a new one
		}

		if updated {
			m.logger.V(0).Info("Updating role binding", "name", rbName, "namespace", namespace)
			if err := m.client.Update(ctx, roleBinding); err != nil {
				return nil, fmt.Errorf("failed to update role binding: %w", err)
			}
		}
	}

	return roleBinding, nil
}
