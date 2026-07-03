package namespace

import (
	"context"
	"fmt"
	"regexp"

	v1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	ManagedByLabel       = "quix.io/managed-by"
	OperatorName         = "quix-environment-operator"
	LabelEnvironmentID   = "quix.io/environment-id"
	LabelEnvironmentName = "quix.io/environment-name"
	// PlatformManagedByLabel marks namespaces for the platform trust-manager custom-CA Bundle (Quix 73259).
	PlatformManagedByLabel = "ManagedBy"
	PlatformManagedByValue = "Quix"
	AnnotationCreatedBy    = "quix.io/created-by"
	AnnotationResourceName = "quix.io/environment-resource-name"
)

// DefaultManager implements the NamespaceManager interface
type DefaultManager struct {
	client   client.Client
	recorder record.EventRecorder
	config   config.ConfigProvider
	// envIdRegexp is the compiled environment ID validation pattern, or nil when no regex is
	// configured. The pattern is immutable startup config, so it is compiled once here.
	envIdRegexp *regexp.Regexp
}

// NewManager creates a new default namespace manager
func NewManager(
	client client.Client,
	recorder record.EventRecorder,
	config config.ConfigProvider,
) *DefaultManager {
	m := &DefaultManager{
		client:   client,
		recorder: recorder,
		config:   config,
	}
	// Compile the immutable environment ID regex once. The pattern is validated compilable at
	// config load, so a compile error here is unreachable; leave envIdRegexp nil and fall back
	// to per-call compilation defensively rather than changing the constructor signature.
	if pattern := config.GetEnvironmentRegex(); pattern != "" {
		if compiled, err := regexp.Compile(pattern); err == nil {
			m.envIdRegexp = compiled
		}
	}
	return m
}

// Exists checks if a namespace exists for an environment
func (m *DefaultManager) Exists(ctx context.Context, env *v1.Environment) (bool, error) {
	namespaceName := m.GetNamespaceName(env)
	namespace := &corev1.Namespace{}
	err := m.client.Get(ctx, types.NamespacedName{Name: namespaceName}, namespace)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Create creates a new namespace for the environment
func (m *DefaultManager) create(ctx context.Context, env *v1.Environment) (*corev1.Namespace, error) {
	namespaceName := m.GetNamespaceName(env)
	isValid, err := m.isValidEnvironmentId(env.Spec.Id)
	if err != nil {
		return nil, fmt.Errorf("failed to validate environment ID: %w", err)
	}

	if !isValid {
		return nil, fmt.Errorf("invalid environment ID: %s", env.Spec.Id)
	}

	logger := log.FromContext(ctx).WithValues("namespace", namespaceName)
	logger.V(1).Info("Attempting to create namespace")

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespaceName,
		},
	}

	m.ApplyMetadata(env, namespace)

	if err := m.client.Create(ctx, namespace); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.V(1).Info("Namespace already exists")

			existingNs := &corev1.Namespace{}
			if getErr := m.client.Get(ctx, types.NamespacedName{Name: namespaceName}, existingNs); getErr != nil {
				return nil, fmt.Errorf("failed to get existing namespace after AlreadyExists error: %w", getErr)
			}

			// Check if the namespace is managed by us before making changes.
			// Operate on the already-fetched object to avoid an extra Get / TOCTOU gap.
			if existingNs.Labels == nil || existingNs.Labels[ManagedByLabel] != OperatorName {
				return nil, fmt.Errorf("cannot use existing namespace %s: %w", namespaceName, ErrNamespaceNotManaged)
			}

			// Reject adoption when the namespace already belongs to a different Environment.
			if !m.isOwnedBy(existingNs, env) {
				m.recorder.Eventf(env, corev1.EventTypeWarning, "NamespaceOwnershipConflict",
					"Namespace %s is owned by a different environment (id=%q name=%q) and will not be adopted",
					namespaceName, existingNs.Labels[LabelEnvironmentID], existingNs.Labels[LabelEnvironmentName])
				return nil, fmt.Errorf("cannot adopt namespace %s: %w (id=%q name=%q)",
					namespaceName, ErrNamespaceOwnershipConflict, existingNs.Labels[LabelEnvironmentID], existingNs.Labels[LabelEnvironmentName])
			}

			if m.ApplyMetadata(env, existingNs) {
				logger.V(0).Info("Updating metadata for existing namespace")
				if updateErr := m.client.Update(ctx, existingNs); updateErr != nil {
					return nil, fmt.Errorf("failed to update existing namespace metadata: %w", updateErr)
				}
			}
			return existingNs, nil
		}
		return nil, fmt.Errorf("failed to create namespace: %w", err)
	}

	logger.V(0).Info("Namespace created successfully")
	m.recorder.Eventf(env, corev1.EventTypeNormal, "NamespaceCreated", "Created namespace %s", namespaceName)
	return namespace, nil
}

// Update updates an existing namespace
func (m *DefaultManager) update(ctx context.Context, env *v1.Environment) error {
	namespaceName := m.GetNamespaceName(env)
	logger := log.FromContext(ctx).WithValues("namespace", namespaceName)
	logger.V(1).Info("Updating namespace")

	namespace, err := m.Get(ctx, env)
	if err != nil {
		return err
	}

	// Check if the namespace is managed by us before making changes.
	// Operate on the already-fetched object to avoid an extra Get / TOCTOU gap.
	if namespace.Labels == nil || namespace.Labels[ManagedByLabel] != OperatorName {
		return fmt.Errorf("cannot update namespace %s: %w", namespaceName, ErrNamespaceNotManaged)
	}

	// Reject when the namespace already belongs to a different Environment.
	if !m.isOwnedBy(namespace, env) {
		m.recorder.Eventf(env, corev1.EventTypeWarning, "NamespaceOwnershipConflict",
			"Namespace %s is owned by a different environment (id=%q name=%q) and will not be updated",
			namespaceName, namespace.Labels[LabelEnvironmentID], namespace.Labels[LabelEnvironmentName])
		return fmt.Errorf("cannot adopt namespace %s: %w (id=%q name=%q)",
			namespaceName, ErrNamespaceOwnershipConflict, namespace.Labels[LabelEnvironmentID], namespace.Labels[LabelEnvironmentName])
	}

	if m.ApplyMetadata(env, namespace) {
		if err := m.client.Update(ctx, namespace); err != nil {
			return fmt.Errorf("failed to update namespace: %w", err)
		}
		logger.V(0).Info("Namespace updated successfully")
	} else {
		logger.V(1).Info("No updates needed for namespace")
	}

	return nil
}

// Delete deletes a namespace for an environment
func (m *DefaultManager) Delete(ctx context.Context, env *v1.Environment) error {
	namespaceName := m.GetNamespaceName(env)
	logger := log.FromContext(ctx).WithValues("namespace", namespaceName)
	logger.V(1).Info("Attempting to delete namespace")

	// Check if the namespace exists
	namespace, err := m.Get(ctx, env)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("Namespace already deleted")
			return nil
		}
		return fmt.Errorf("failed to get namespace: %w", err)
	}

	// Check if the namespace is managed by this operator before deleting
	if namespace.Labels == nil || namespace.Labels[ManagedByLabel] != OperatorName {
		logger.V(0).Info("Namespace is not managed by this operator, skipping deletion")
		m.recorder.Eventf(env, corev1.EventTypeWarning, "NamespaceNotManaged",
			"Namespace %s is not managed by this operator and will not be deleted", namespaceName)
		return fmt.Errorf("namespace %s is %w and will not be deleted", namespaceName, ErrNamespaceNotManaged)
	}

	// Verify environment-id ownership before deleting (defense-in-depth against a
	// pre-staged/hijacked namespace carrying our management label but a foreign identity).
	//
	// A namespace whose identity does not match this Environment is not ours to delete, so we
	// return an error and leave it untouched. The reconciler's handleDeletion recognises this
	// error (alongside the "not managed" case) and proceeds straight to finalizer removal
	// instead of waiting for a namespace deletion that will never happen — so the foreign
	// namespace is preserved and our own Environment still finalizes rather than wedging.
	if !m.isOwnedBy(namespace, env) {
		logger.V(0).Info("Namespace environment-id does not match this environment, refusing deletion")
		m.recorder.Eventf(env, corev1.EventTypeWarning, "NamespaceEnvironmentIDMismatch",
			"Namespace %s is owned by a different environment (id=%q name=%q) and will not be deleted",
			namespaceName, namespace.Labels[LabelEnvironmentID], namespace.Labels[LabelEnvironmentName])
		return fmt.Errorf("namespace %s %w, refusing deletion", namespaceName, ErrNamespaceEnvironmentIDMismatch)
	}

	// Proceed with deletion
	if err := m.client.Delete(ctx, namespace); err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("Namespace already deleted")
			return nil
		}
		return fmt.Errorf("failed to delete namespace: %w", err)
	}

	logger.V(0).Info("Namespace deletion initiated")
	return nil
}

// Get retrieves a namespace for an environment
func (m *DefaultManager) Get(ctx context.Context, env *v1.Environment) (*corev1.Namespace, error) {
	namespaceName := m.GetNamespaceName(env)
	logger := log.FromContext(ctx)
	namespace := &corev1.Namespace{}
	err := m.client.Get(ctx, types.NamespacedName{Name: namespaceName}, namespace)

	if err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("Namespace not found", "namespaceName", namespaceName)
			return nil, err
		}
		return nil, fmt.Errorf("failed to get namespace %s: %w", namespaceName, err)
	}

	logger.V(1).Info("Found namespace", "namespaceName", namespaceName, "uid", namespace.UID)
	return namespace, nil
}

// isOwnedBy reports whether an already-fetched, operator-managed namespace belongs to env.
// Ownership is determined by the identity labels (environment-id and environment-name).
// A managed namespace whose identity labels are BOTH empty/absent is treated as adoptable
// (first-time claim, e.g. legacy namespaces created before identity labels were applied);
// otherwise both identity labels must match exactly.
func (m *DefaultManager) isOwnedBy(ns *corev1.Namespace, env *v1.Environment) bool {
	existingID := ns.Labels[LabelEnvironmentID]
	existingName := ns.Labels[LabelEnvironmentName]

	// Adoptable: managed namespace with no identity yet.
	if existingID == "" && existingName == "" {
		return true
	}

	return existingID == env.Spec.Id && existingName == env.Name
}

// IsDeleting checks if a namespace is deleting
func (m *DefaultManager) IsDeleting(ctx context.Context, env *v1.Environment) (bool, error) {
	namespaceName := m.GetNamespaceName(env)
	namespace := &corev1.Namespace{}
	err := m.client.Get(ctx, types.NamespacedName{Name: namespaceName}, namespace)
	if err != nil {
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}

	return namespace.DeletionTimestamp != nil, nil
}

// ApplyMetadata applies standard labels and annotations to a namespace
func (m *DefaultManager) ApplyMetadata(env *v1.Environment, namespace *corev1.Namespace) bool {
	needsUpdate := false

	// Ensure namespace has labels map initialized
	if namespace.Labels == nil {
		namespace.Labels = make(map[string]string)
		needsUpdate = true
	}

	// Ensure required labels are set
	requiredLabels := map[string]string{
		ManagedByLabel:         OperatorName,
		LabelEnvironmentID:     env.Spec.Id,
		LabelEnvironmentName:   env.Name,
		PlatformManagedByLabel: PlatformManagedByValue,
	}

	for key, value := range requiredLabels {
		if namespace.Labels[key] != value {
			namespace.Labels[key] = value
			needsUpdate = true
		}
	}

	// Define a list of protected labels that cannot be overridden
	protectedLabels := []string{
		ManagedByLabel,
		LabelEnvironmentID,
		LabelEnvironmentName,
		PlatformManagedByLabel,
		"kubernetes.io/metadata.name",
		"control-plane",
	}

	// Apply custom labels from Environment
	for key, value := range env.Spec.Labels {
		// Skip protected labels
		if isProtectedKey(key, protectedLabels) {
			continue
		}

		// Enforce quix.io/ prefix for custom labels
		if !isValidLabelPrefix(key) {
			continue
		}

		if namespace.Labels[key] != value {
			namespace.Labels[key] = value
			needsUpdate = true
		}
	}

	// Ensure namespace has annotations map initialized
	if namespace.Annotations == nil {
		namespace.Annotations = make(map[string]string)
		needsUpdate = true
	}

	// Ensure required annotations are set
	requiredAnnotations := map[string]string{
		AnnotationCreatedBy:    OperatorName,
		AnnotationResourceName: env.Name,
	}

	for key, value := range requiredAnnotations {
		if namespace.Annotations[key] != value {
			namespace.Annotations[key] = value
			needsUpdate = true
		}
	}

	// Define a list of protected annotations that cannot be overridden
	protectedAnnotations := []string{
		AnnotationCreatedBy,
		AnnotationResourceName,
	}

	// Apply custom annotations from Environment
	for key, value := range env.Spec.Annotations {
		// Skip protected annotations
		if isProtectedKey(key, protectedAnnotations) {
			continue
		}

		// Enforce quix.io/ prefix for custom annotations
		if !isValidLabelPrefix(key) {
			continue
		}

		if namespace.Annotations[key] != value {
			namespace.Annotations[key] = value
			needsUpdate = true
		}
	}

	if ensureOwnerReference(env, namespace) {
		needsUpdate = true
	}

	return needsUpdate
}

// ensureOwnerReference sets a controller owner reference to the Environment so Kubernetes
// garbage collection can reclaim the namespace as a fallback if the operator is down during
// Environment deletion. The finalizer-driven Delete() remains the primary cleanup path. The
// Environment is cluster-scoped like the Namespace, so this owner reference is valid. The
// RoleBinding is intentionally NOT given an Environment owner reference: it lives in this
// namespace and is reclaimed automatically when the namespace is deleted.
func ensureOwnerReference(env *v1.Environment, namespace *corev1.Namespace) bool {
	if env.UID == "" {
		return false
	}

	expected := environmentOwnerReference(env)
	changed := false
	found := false
	ownerReferences := namespace.OwnerReferences[:0]

	for _, ref := range namespace.OwnerReferences {
		if ref.APIVersion != expected.APIVersion || ref.Kind != expected.Kind {
			ownerReferences = append(ownerReferences, ref)
			continue
		}

		if found {
			changed = true
			continue
		}

		if ref.UID != expected.UID ||
			ref.Name != expected.Name ||
			ref.Controller == nil || !*ref.Controller ||
			ref.BlockOwnerDeletion == nil || !*ref.BlockOwnerDeletion {
			ownerReferences = append(ownerReferences, expected)
			changed = true
		} else {
			ownerReferences = append(ownerReferences, ref)
		}

		found = true
	}

	if !found {
		ownerReferences = append(ownerReferences, expected)
		changed = true
	}

	if changed {
		namespace.OwnerReferences = ownerReferences
	}
	return changed
}

func environmentOwnerReference(env *v1.Environment) metav1.OwnerReference {
	isController := true
	blockOwnerDeletion := true

	return metav1.OwnerReference{
		APIVersion:         v1.GroupVersion.String(),
		Kind:               "Environment",
		Name:               env.Name,
		UID:                env.UID,
		Controller:         &isController,
		BlockOwnerDeletion: &blockOwnerDeletion,
	}
}

// isProtectedKey checks if the given key is in the list of protected keys. It is used for both
// protected labels and protected annotations, which share identical membership semantics.
func isProtectedKey(key string, protectedKeys []string) bool {
	for _, protectedKey := range protectedKeys {
		if key == protectedKey {
			return true
		}
	}
	return false
}

// isValidLabelPrefix checks if a label or annotation has a valid prefix (must be quix.io/)
func isValidLabelPrefix(key string) bool {
	const validPrefix = "quix.io/"
	return len(key) > len(validPrefix) && key[:len(validPrefix)] == validPrefix
}

// GetNamespaceName returns the standardized name for the environment namespace.
// It is a pure getter: it returns the persisted ResourceName when set, otherwise
// the computed convention name. It never mutates env.Status.
func (m *DefaultManager) GetNamespaceName(env *v1.Environment) string {
	// Use stored namespace name if available
	if env.Status.NamespaceStatus != nil && env.Status.NamespaceStatus.ResourceName != "" {
		return env.Status.NamespaceStatus.ResourceName
	}

	// Otherwise use the default naming convention
	return fmt.Sprintf("%s%s", env.Spec.Id, m.config.GetNamespaceSuffix())
}

// isValidEnvironmentId checks if the environment ID matches the configured regex pattern
func (m *DefaultManager) isValidEnvironmentId(envId string) (bool, error) {
	// If no regex is configured, all IDs are valid
	if m.config.GetEnvironmentRegex() == "" {
		return true, nil
	}

	// Use the pattern compiled once at construction. It is nil only in the unreachable case where
	// the pre-validated pattern failed to compile there; fall back to per-call compilation so
	// validation semantics are preserved.
	pattern := m.envIdRegexp
	if pattern == nil {
		var err error
		pattern, err = regexp.Compile(m.config.GetEnvironmentRegex())
		if err != nil {
			return false, fmt.Errorf("invalid environment regex pattern: %w", err)
		}
	}

	// Check if the environment ID matches the pattern
	return pattern.MatchString(envId), nil
}

// Reconcile creates or updates a namespace for an environment
func (m *DefaultManager) Reconcile(ctx context.Context, env *v1.Environment) (*corev1.Namespace, error) {
	namespaceName := m.GetNamespaceName(env)
	logger := log.FromContext(ctx).WithValues("namespace", namespaceName)
	logger.V(1).Info("Reconciling namespace")

	// Check if namespace exists
	exists, err := m.Exists(ctx, env)
	if !exists {
		if err != nil {
			return nil, fmt.Errorf("failed to get namespace: %w", err)
		}
		// Create namespace if it doesn't exist
		newNamespace, err := m.create(ctx, env)
		if err != nil {
			return nil, fmt.Errorf("failed to create namespace: %w", err)
		}
		return newNamespace, nil
	}

	// Update existing namespace
	err = m.update(ctx, env)
	if err != nil {
		return nil, fmt.Errorf("failed to update namespace: %w", err)
	}

	// Refresh namespace status
	updatedNs := &corev1.Namespace{}
	err = m.client.Get(ctx, types.NamespacedName{Name: namespaceName}, updatedNs)
	if err != nil {
		return nil, fmt.Errorf("failed to get updated namespace: %w", err)
	}

	return updatedNs, nil
}
