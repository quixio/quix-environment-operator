package controllers

import (
	"context"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
	"github.com/quix-analytics/quix-environment-operator/internal/namespaces"
	"github.com/quix-analytics/quix-environment-operator/internal/status"
)

const (
	// Finalizer for environment resources
	EnvironmentFinalizer = "quix.io/environment-finalizer"
	ManagedByLabel       = "quix.io/managed-by"
	OperatorName         = "quix-environment-operator"
)

// EnvironmentReconciler reconciles an Environment object
type EnvironmentReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Config   *config.OperatorConfig

	// Private fields with required dependencies
	namespaceManager namespaces.NamespaceManager
	statusUpdater    status.StatusUpdater
}

// NewEnvironmentReconciler creates a new reconciler with required dependencies
func NewEnvironmentReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
	config *config.OperatorConfig,
	namespaceManager namespaces.NamespaceManager,
	statusUpdater status.StatusUpdater,
) (*EnvironmentReconciler, error) {
	if namespaceManager == nil {
		return nil, fmt.Errorf("NamespaceManager is required but was not provided")
	}
	if statusUpdater == nil {
		return nil, fmt.Errorf("StatusUpdater is required but was not provided")
	}

	return &EnvironmentReconciler{
		Client:           client,
		Scheme:           scheme,
		Recorder:         recorder,
		Config:           config,
		namespaceManager: namespaceManager,
		statusUpdater:    statusUpdater,
	}, nil
}

func (r *EnvironmentReconciler) NamespaceManager() namespaces.NamespaceManager {
	return r.namespaceManager
}

func (r *EnvironmentReconciler) StatusUpdater() status.StatusUpdater {
	return r.statusUpdater
}

// +kubebuilder:rbac:groups=quix.io,resources=environments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=quix.io,resources=environments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=quix.io,resources=environments/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;create;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;create;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

func (r *EnvironmentReconciler) getNamespace(ctx context.Context, name string) (*corev1.Namespace, error) {
	logger := log.FromContext(ctx)
	namespace := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: name}, namespace)

	if err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("Namespace not found", "namespaceName", name)
			return nil, err
		}
		logger.Error(err, "Failed to get namespace", "namespaceName", name)
		return nil, err
	}

	logger.V(2).Info("Found namespace", "namespaceName", name, "uid", namespace.UID)
	return namespace, nil
}

// Reconcile handles the reconciliation logic for Environment resources
func (r *EnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Environment", "request", req.NamespacedName)

	env := &quixiov1.Environment{}
	if err := r.Get(ctx, req.NamespacedName, env); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Environment resource not found, ignoring")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get Environment")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	logger.V(1).Info("Retrieved Environment",
		"phase", env.Status.Phase,
		"hasFinalize", controllerutil.ContainsFinalizer(env, EnvironmentFinalizer),
		"isBeingDeleted", !env.DeletionTimestamp.IsZero())

	// Handle finalizer
	if !controllerutil.ContainsFinalizer(env, EnvironmentFinalizer) {
		logger.Info("Adding finalizer")
		controllerutil.AddFinalizer(env, EnvironmentFinalizer)
		if err := r.Update(ctx, env); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Handle deletion
	if !env.DeletionTimestamp.IsZero() {
		if env.Status.Phase != quixiov1.PhaseDeleting {
			statusErr := r.StatusUpdater().UpdateStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
				status.Phase = quixiov1.PhaseDeleting
			})
			if statusErr != nil {
				logger.Error(statusErr, "Failed to update phase to Deleting")
			}
		}
		return r.handleDeletion(ctx, env)
	}

	// Set phase to Creating if it's not set
	if env.Status.Phase == "" {
		statusErr := r.StatusUpdater().UpdateStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
			status.Phase = quixiov1.PhaseCreating
		})
		if statusErr != nil {
			logger.Error(statusErr, "Failed to update phase to Creating")
		}
	}

	// Validate the environment
	if validationErr := r.validateEnvironment(ctx, env); validationErr != nil {
		r.Recorder.Eventf(env, corev1.EventTypeWarning, "ValidationFailed", "Environment validation failed: %v", validationErr)
		r.StatusUpdater().SetErrorStatus(ctx, env, quixiov1.PhaseCreateFailed,
			status.ConditionTypeReady, validationErr, "Environment validation failed")
		return ctrl.Result{}, nil
	}

	// Verify namespace exists and create if needed
	namespaceName := fmt.Sprintf("%s%s", env.Spec.Id, r.Config.NamespaceSuffix)
	namespace, nsErr := r.getNamespace(ctx, namespaceName)

	if nsErr != nil && errors.IsNotFound(nsErr) {
		// Create the namespace
		if createErr := r.createNamespace(ctx, env, namespaceName); createErr != nil {
			r.StatusUpdater().SetErrorStatus(ctx, env, quixiov1.PhaseCreating,
				status.ConditionTypeNamespaceCreated, createErr, "Retrying namespace creation")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		// Get the created namespace
		namespace, nsErr = r.getNamespace(ctx, namespaceName)
		if nsErr != nil {
			if errors.IsNotFound(nsErr) {
				logger.V(1).Info("Namespace not yet visible after creation request")
				return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil
			}
			logger.Error(nsErr, "Failed to get namespace after creation")
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}

		// Set condition after namespace creation
		r.StatusUpdater().SetSuccessStatus(ctx, env, status.ConditionTypeNamespaceCreated,
			fmt.Sprintf("Created namespace %s", namespaceName))
	} else if nsErr != nil {
		logger.Error(nsErr, "Failed to get namespace")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Check if the fetched namespace is actually managed by this operator
	if namespace != nil && !r.NamespaceManager().IsNamespaceDeleted(namespace, nsErr) {
		managedLabel, exists := namespace.Labels[ManagedByLabel]
		if !exists || managedLabel != OperatorName {
			// Namespace exists but is not managed by us. This is a collision.
			collisionErr := fmt.Errorf("namespace '%s' already exists and is not managed by this operator", namespaceName)
			r.Recorder.Eventf(env, corev1.EventTypeWarning, "CollisionDetected", "Namespace %s already exists and is not managed by this operator", namespaceName)

			// Determine appropriate phase based on current state
			phase := env.Status.Phase
			if phase == "" || phase == quixiov1.PhaseCreating {
				phase = quixiov1.PhaseCreateFailed
			} else {
				phase = quixiov1.PhaseUpdateFailed // If it was already Ready/Updating, fail the update
			}

			r.StatusUpdater().SetErrorStatus(ctx, env, phase,
				status.ConditionTypeReady, collisionErr, "Namespace collision detected")

			return ctrl.Result{}, nil // Stop reconciliation for this Environment
		}
		// If label exists and matches, proceed normally
	}

	// Check if the namespace is pending deletion
	if r.NamespaceManager().IsNamespaceDeleted(namespace, nsErr) {
		// If in deletion flow, proceed normally
		if !env.DeletionTimestamp.IsZero() {
			logger.Info("Namespace already deleted or being deleted")

			// Update status to reflect that resources are being cleaned up
			r.StatusUpdater().SetSuccessStatus(ctx, env, status.ConditionTypeReady, "Namespace deletion in progress")

			// Refetch the latest version of the Environment resource to avoid conflicts
			latestEnv := &quixiov1.Environment{}
			if err := r.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, latestEnv); err != nil {
				if errors.IsNotFound(err) {
					logger.Info("Environment resource already deleted")
					return ctrl.Result{}, nil // Already deleted, nothing more to do
				}
				logger.Error(err, "Failed to refetch Environment before finalizer removal")
				return ctrl.Result{RequeueAfter: 2 * time.Second}, err
			}

			// Remove our finalizer to allow the Environment to be deleted
			controllerutil.RemoveFinalizer(latestEnv, EnvironmentFinalizer)
			if err := r.Update(ctx, latestEnv); err != nil {
				// Check if the error is due to the resource being modified again
				if errors.IsConflict(err) {
					logger.Info("Conflict detected while removing finalizer, requeuing")
					return ctrl.Result{Requeue: true}, nil
				}
				logger.Error(err, "Failed to remove finalizer")
				return ctrl.Result{RequeueAfter: 2 * time.Second}, err
			}

			logger.Info("Finalizer removed, allowing Environment resource to be deleted")
			return ctrl.Result{}, nil
		} else {
			// In creation/update flow, wait for namespace to finish deletion before recreating
			// Determine appropriate phase based on current state to preserve operation context
			phase := env.Status.Phase
			if phase == "" || phase == quixiov1.PhaseCreating {
				phase = quixiov1.PhaseCreating
			} else {
				phase = quixiov1.PhaseUpdating
			}

			logger.Info("Namespace is being deleted, will recreate later",
				"namespace", namespaceName,
				"phase", phase)

			r.StatusUpdater().SetErrorStatus(ctx, env, phase,
				status.ConditionTypeNamespaceCreated, fmt.Errorf("namespace is being deleted"),
				"Waiting for namespace deletion to complete")

			// Requeue to check again later
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	// Always update namespace metadata to ensure changes propagate correctly
	if updateErr := r.NamespaceManager().UpdateMetadata(ctx, env, namespace); updateErr != nil {
		r.Recorder.Eventf(env, corev1.EventTypeWarning, "UpdateFailed",
			"Failed to update namespace metadata: %v", updateErr)

		r.StatusUpdater().SetErrorStatus(ctx, env, quixiov1.PhaseUpdateFailed,
			status.ConditionTypeReady, updateErr, "Failed to update namespace metadata")

		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Create or verify role binding exists
	if err := r.createRoleBinding(ctx, env, namespaceName); err != nil {
		// Use appropriate phase based on current state
		phase := env.Status.Phase
		if phase == "" || phase == quixiov1.PhaseCreating {
			phase = quixiov1.PhaseCreating
		} else {
			phase = quixiov1.PhaseUpdating
		}

		r.StatusUpdater().SetErrorStatus(ctx, env, phase,
			status.ConditionTypeRoleBindingCreated, err, "Failed to create role binding")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Set RoleBindingCreated condition
	r.StatusUpdater().SetSuccessStatus(ctx, env, status.ConditionTypeRoleBindingCreated,
		fmt.Sprintf("Created RoleBinding in namespace %s", namespaceName))

	// Update status to Ready
	statusErr := r.StatusUpdater().UpdateStatus(ctx, env, func(status *quixiov1.EnvironmentStatus) {
		if status.Namespace == "" {
			status.Namespace = namespaceName
		}
		status.Phase = quixiov1.PhaseReady
		status.ErrorMessage = ""
	})
	if statusErr != nil {
		logger.Error(statusErr, "Failed to update status")
		return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
	}

	r.StatusUpdater().SetSuccessStatus(ctx, env, status.ConditionTypeReady, "Environment provisioned successfully")
	logger.Info("Reconciliation completed", "phase", env.Status.Phase, "namespace", namespaceName)
	return ctrl.Result{}, nil
}

// validateEnvironment validates the Environment spec
func (r *EnvironmentReconciler) validateEnvironment(ctx context.Context, env *quixiov1.Environment) error {
	if env.Spec.Id == "" {
		return fmt.Errorf("id is required")
	}

	// Generate the namespace name
	namespaceName := fmt.Sprintf("%s%s", env.Spec.Id, r.Config.NamespaceSuffix)

	// Check if the namespace name is too long
	if len(namespaceName) > 63 {
		return fmt.Errorf("generated namespace name '%s' exceeds the 63 character limit", namespaceName)
	}

	return nil
}

// createNamespace creates a new namespace for the environment
func (r *EnvironmentReconciler) createNamespace(ctx context.Context, env *quixiov1.Environment, namespaceName string) error {
	logger := log.FromContext(ctx)
	logger.Info("Creating namespace", "name", namespaceName)

	// Check if the namespace exists first - it might have been created already
	existingNs := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: namespaceName}, existingNs)
	if err == nil {
		logger.V(1).Info("Namespace already exists")
		return nil
	} else if !errors.IsNotFound(err) {
		logger.Error(err, "Error checking for existing namespace")
		return err
	}

	// Create the namespace
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespaceName,
		},
	}

	// Apply common labels and annotations
	r.NamespaceManager().ApplyMetadata(env, namespace)

	// Create the namespace
	if err := r.Create(ctx, namespace); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.V(1).Info("Namespace already exists (created concurrently)")
			return nil
		}
		logger.Error(err, "Failed to create namespace")
		return err
	}

	logger.Info("Namespace created successfully")
	return nil
}

// createRoleBinding creates a RoleBinding in the environment namespace
func (r *EnvironmentReconciler) createRoleBinding(ctx context.Context, env *quixiov1.Environment, namespaceName string) error {
	logger := log.FromContext(ctx)
	logger.V(1).Info("Setting up role binding", "namespace", namespaceName)

	// Check if the RoleBinding already exists
	existingRb := &rbacv1.RoleBinding{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      "quix-environment-access",
		Namespace: namespaceName,
	}, existingRb)

	if err == nil {
		logger.V(1).Info("RoleBinding already exists")
		return nil
	} else if !errors.IsNotFound(err) {
		logger.Error(err, "Error checking for existing RoleBinding")
		return err
	}

	// Define the RoleBinding
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "quix-environment-access",
			Namespace: namespaceName,
			Labels: map[string]string{
				"quix.io/managed-by": "quix-environment-operator",
			},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      r.Config.ServiceAccountName,
				Namespace: r.Config.ServiceAccountNamespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     r.Config.ClusterRoleName,
		},
	}

	// Create the RoleBinding
	createErr := r.Create(ctx, rb)
	if createErr != nil {
		if errors.IsAlreadyExists(createErr) {
			logger.V(1).Info("RoleBinding already exists (created concurrently)")
			return nil
		}
		logger.Error(createErr, "Failed to create RoleBinding")
		return createErr
	}

	logger.Info("RoleBinding created")
	return nil
}

// handleDeletion handles the deletion of an Environment resource
func (r *EnvironmentReconciler) handleDeletion(ctx context.Context, env *quixiov1.Environment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling environment deletion")

	namespaceName := fmt.Sprintf("%s%s", env.Spec.Id, r.Config.NamespaceSuffix)

	// Check if the namespace exists
	namespace, nsErr := r.getNamespace(ctx, namespaceName)

	// If namespace is already gone or being deleted, we can proceed with finalizer removal
	if r.NamespaceManager().IsNamespaceDeleted(namespace, nsErr) {
		logger.Info("Namespace already deleted or being deleted")

		// Update status conditions
		r.StatusUpdater().SetSuccessStatus(ctx, env, status.ConditionTypeNamespaceDeleted, "Namespace deletion completed")

		// Refetch the latest version of the Environment resource to avoid conflicts
		latestEnv := &quixiov1.Environment{}
		if err := r.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, latestEnv); err != nil {
			if errors.IsNotFound(err) {
				logger.Info("Environment resource already deleted")
				return ctrl.Result{}, nil // Already deleted, nothing more to do
			}
			logger.Error(err, "Failed to refetch Environment before finalizer removal")
			return ctrl.Result{RequeueAfter: 2 * time.Second}, err
		}

		// Remove our finalizer to allow the Environment to be deleted
		controllerutil.RemoveFinalizer(latestEnv, EnvironmentFinalizer)
		if err := r.Update(ctx, latestEnv); err != nil {
			// Check if the error is due to the resource being modified again
			if errors.IsConflict(err) {
				logger.Info("Conflict detected while removing finalizer, requeuing")
				return ctrl.Result{Requeue: true}, nil
			}
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{RequeueAfter: 2 * time.Second}, err
		}

		logger.Info("Finalizer removed, allowing Environment resource to be deleted")
		return ctrl.Result{}, nil
	}

	// Namespace exists and is not being deleted, try to delete it ONLY if managed by us
	if nsErr == nil {
		managedLabel, exists := namespace.Labels[ManagedByLabel]
		if exists && managedLabel == OperatorName {
			// Namespace is managed by us, proceed with deletion
			logger.Info("Deleting managed namespace")

			// Set deletion timestamp update to help garbage collection
			namespace.SetFinalizers(nil) // Attempt to remove finalizers we might not own
			if err := r.Update(ctx, namespace); err != nil {
				if !errors.IsNotFound(err) {
					logger.Error(err, "Failed to remove finalizers from namespace")
				}
			}

			if err := r.Delete(ctx, namespace); err != nil {
				if errors.IsNotFound(err) {
					logger.Info("Managed namespace already deleted")
				} else {
					logger.Error(err, "Failed to delete managed namespace")
					return ctrl.Result{RequeueAfter: 5 * time.Second}, err
				}
			}

			// Record event for deletion
			r.Recorder.Event(env, corev1.EventTypeNormal, "NamespaceDeleted", "Managed namespace deletion initiated")
			// Requeue to check later if the namespace is fully deleted
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		} else {
			// Namespace exists but is not managed by us. Skip deletion.
			logger.Info("Skipping deletion of unmanaged namespace", "namespace", namespaceName)
			// Proceed to finalizer removal as the unmanaged namespace is not our responsibility
		}
	}

	logger.Info("Proceeding to remove finalizer as namespace is gone or unmanaged")

	// Refetch the latest version of the Environment resource to avoid conflicts
	latestEnv := &quixiov1.Environment{}
	if err := r.Get(ctx, types.NamespacedName{Name: env.Name, Namespace: env.Namespace}, latestEnv); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Environment resource already deleted while attempting finalizer removal")
			return ctrl.Result{}, nil // Already deleted, nothing more to do
		}
		logger.Error(err, "Failed to refetch Environment before finalizer removal")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, err
	}

	// Remove our finalizer to allow the Environment to be deleted
	controllerutil.RemoveFinalizer(latestEnv, EnvironmentFinalizer)
	if err := r.Update(ctx, latestEnv); err != nil {
		if !errors.IsNotFound(err) {
			// Check if the error is due to the resource being modified again
			if errors.IsConflict(err) {
				logger.Info("Conflict detected while removing finalizer, requeuing")
				return ctrl.Result{Requeue: true}, nil
			}
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{RequeueAfter: 2 * time.Second}, err
		}
	}

	logger.Info("Finalizer removed, allowing Environment resource to be deleted")
	return ctrl.Result{}, nil
}

func ptr(i int64) *int64 {
	return &i
}

// SetupWithManager sets up the controller with the Manager
func (r *EnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	setupLogger := ctrl.Log.WithName("setup").WithName("Environment")

	// Predicate for watching namespaces
	namespacePredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			ns, ok := e.Object.(*corev1.Namespace)
			if !ok {
				return false
			}
			environmentId, isOurNamespace := ns.Labels["quix.io/environment-id"]
			if isOurNamespace {
				setupLogger.V(1).Info("Namespace created",
					"namespace", ns.Name,
					"environmentId", environmentId)
			}
			return isOurNamespace
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			ns, ok := e.ObjectNew.(*corev1.Namespace)
			if !ok {
				return false
			}
			_, isOurNamespace := ns.Labels["quix.io/environment-id"]
			return isOurNamespace
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			namespace, ok := e.Object.(*corev1.Namespace)
			if !ok {
				return false
			}
			_, isOurNamespace := namespace.Labels["quix.io/environment-id"]
			return isOurNamespace
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}

	// Custom predicate for Environment resources
	environmentPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return true // Always reconcile on creation
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldEnv, oldOk := e.ObjectOld.(*quixiov1.Environment)
			newEnv, newOk := e.ObjectNew.(*quixiov1.Environment)

			if !oldOk || !newOk {
				return false
			}

			// Trigger reconciliation on spec or deletion timestamp changes
			specChanged := !reflect.DeepEqual(oldEnv.Spec, newEnv.Spec)
			timestampChanged := !oldEnv.DeletionTimestamp.Equal(newEnv.DeletionTimestamp)

			return specChanged || timestampChanged
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// We use finalizers, so don't need to trigger on delete events
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}

	// Custom handler to map namespace deletions to their environment resources
	namespaceHandler := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		setupLogger := ctrl.Log.WithName("namespaceMapper")
		namespace, ok := obj.(*corev1.Namespace)
		if !ok {
			setupLogger.Info("Object is not a Namespace, ignoring")
			return nil
		}

		// Get the environment ID from the namespace label
		environmentId, ok := namespace.Labels["quix.io/environment-id"]
		if !ok {
			return nil
		}

		// List all environments to find the matching one
		var environments quixiov1.EnvironmentList
		if err := mgr.GetClient().List(ctx, &environments); err != nil {
			setupLogger.Error(err, "Failed to list environments")
			return nil
		}

		var requests []reconcile.Request
		for _, env := range environments.Items {
			if env.Spec.Id == environmentId {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      env.Name,
						Namespace: env.Namespace,
					},
				})
			}
		}

		return requests
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&quixiov1.Environment{}, builder.WithPredicates(environmentPredicate)).
		Watches(
			&corev1.Namespace{},
			namespaceHandler,
			builder.WithPredicates(namespacePredicate),
		).
		Complete(r)
}
