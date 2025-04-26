package controllers

import (
	"context"
	"reflect"

	"github.com/go-logr/logr"
	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func (r *EnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	setupLogger := ctrl.Log.WithName("setup").WithName("Environment")

	// Set up the field indexer for efficient lookups
	if err := r.setupFieldIndexer(mgr, setupLogger); err != nil {
		return err
	}

	// Create predicates for filtering events
	namespacePredicate := r.createNamespacePredicate()
	environmentPredicate := r.createEnvironmentPredicate(setupLogger)

	// Create handler for mapping namespace events to environment reconcile requests
	namespaceHandler := r.createNamespaceHandler(mgr)

	// Configure and build the controller
	return r.configureController(mgr, environmentPredicate, namespacePredicate, namespaceHandler)
}

func (r *EnvironmentReconciler) configureController(
	mgr ctrl.Manager,
	environmentPredicate predicate.Predicate,
	namespacePredicate predicate.Predicate,
	namespaceHandler handler.EventHandler,
) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&quixiov1.Environment{}, builder.WithPredicates(environmentPredicate)).
		Owns(&corev1.Namespace{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(
			&corev1.Namespace{},
			namespaceHandler,
			builder.WithPredicates(namespacePredicate),
		).
		Complete(r)
}

func (r *EnvironmentReconciler) setupFieldIndexer(mgr ctrl.Manager, logger logr.Logger) error {
	err := mgr.GetFieldIndexer().IndexField(context.Background(), &quixiov1.Environment{}, ".spec.id", func(rawObj client.Object) []string {
		env, ok := rawObj.(*quixiov1.Environment)
		if !ok {
			return nil
		}
		if env.Spec.Id == "" {
			return nil
		}
		return []string{env.Spec.Id}
	})

	if err != nil {
		logger.V(3).Error(err, "Failed to set up index for .spec.id")
		return err
	}

	return nil
}

func (r *EnvironmentReconciler) createNamespacePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			ns, ok := e.Object.(*corev1.Namespace)
			if !ok {
				return false
			}
			_, managed := ns.Labels[ManagedByLabel]
			return managed
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNs, oldOk := e.ObjectOld.(*corev1.Namespace)
			newNs, newOk := e.ObjectNew.(*corev1.Namespace)
			if !oldOk || !newOk {
				return false
			}
			labelsChanged := !reflect.DeepEqual(oldNs.Labels, newNs.Labels)
			deletionChanged := !oldNs.DeletionTimestamp.Equal(newNs.DeletionTimestamp)
			phaseChanged := oldNs.Status.Phase != newNs.Status.Phase

			_, oldManaged := oldNs.Labels[ManagedByLabel]
			_, newManaged := newNs.Labels[ManagedByLabel]
			return (oldManaged || newManaged) && (labelsChanged || deletionChanged || phaseChanged)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			ns, ok := e.Object.(*corev1.Namespace)
			if !ok {
				return false
			}
			_, managed := ns.Labels[ManagedByLabel]
			return managed
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}
}

func (r *EnvironmentReconciler) createEnvironmentPredicate(logger logr.Logger) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return true // Always reconcile on creation
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return r.shouldReconcileOnUpdate(e, logger)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false // We use finalizers, so don't need to trigger on delete events
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}
}

func (r *EnvironmentReconciler) shouldReconcileOnUpdate(e event.UpdateEvent, logger logr.Logger) bool {
	oldEnv, oldOk := e.ObjectOld.(*quixiov1.Environment)
	newEnv, newOk := e.ObjectNew.(*quixiov1.Environment)

	if !oldOk || !newOk {
		return false
	}

	specChanged := !reflect.DeepEqual(oldEnv.Spec, newEnv.Spec)
	timestampChanged := !oldEnv.DeletionTimestamp.Equal(newEnv.DeletionTimestamp)
	generationChanged := oldEnv.Generation != newEnv.Generation

	if specChanged {
		logger.V(1).Info("Reconciling due to spec change", "environment", newEnv.Name)
	} else if timestampChanged {
		logger.V(1).Info("Reconciling due to deletion timestamp change", "environment", newEnv.Name)
	} else if generationChanged {
		logger.V(1).Info("Reconciling due to metadata.generation change", "environment", newEnv.Name)
	}

	return specChanged || timestampChanged || generationChanged
}

func (r *EnvironmentReconciler) createNamespaceHandler(mgr ctrl.Manager) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		nsMapperLogger := ctrl.Log.WithName("namespaceMapper")
		namespace, ok := obj.(*corev1.Namespace)
		if !ok {
			nsMapperLogger.V(1).Info("Object is not a Namespace, ignoring map request")
			return nil
		}

		environmentId, ok := namespace.Labels[LabelEnvironmentID]
		if !ok {
			nsMapperLogger.V(1).Info("Namespace does not have environment-id label, ignoring", "namespace", namespace.Name)
			return nil
		}

		return r.findEnvironmentsForNamespace(ctx, mgr, namespace, environmentId, nsMapperLogger)
	})
}

func (r *EnvironmentReconciler) findEnvironmentsForNamespace(
	ctx context.Context,
	mgr ctrl.Manager,
	namespace *corev1.Namespace,
	environmentId string,
	logger logr.Logger,
) []reconcile.Request {
	environmentCRDNamespace := namespace.Annotations[AnnotationEnvironmentCRDNamespace]
	listOptions := []client.ListOption{}

	if environmentCRDNamespace != "" {
		listOptions = append(listOptions, client.InNamespace(environmentCRDNamespace))
	}

	listOptions = append(listOptions, client.MatchingFields{".spec.id": environmentId})

	var environments quixiov1.EnvironmentList
	if err := mgr.GetClient().List(ctx, &environments, listOptions...); err != nil {
		logger.V(3).Error(err, "Failed to list environments for namespace event", "environmentId", environmentId)
		return nil
	}

	var requests []reconcile.Request
	for _, env := range environments.Items {
		logger.Info("Queueing reconcile request for Environment due to namespace event",
			"environment", env.Name,
			"namespace", env.Namespace,
			"triggeringNamespace", namespace.Name)
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      env.Name,
				Namespace: env.Namespace,
			},
		})
	}

	if len(requests) == 0 {
		logger.V(1).Info("No matching Environment found for namespace event", "environmentId", environmentId, "namespace", namespace.Name)
	}

	return requests
}
