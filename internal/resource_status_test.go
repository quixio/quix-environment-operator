//go:build integration
// +build integration

package environment

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("Environment Resource Status Phases", func() {
	// Increase timeouts to give environment controller more time to reconcile
	const timeout = time.Second * 10
	const interval = time.Millisecond * 250
	const deletionTimeout = time.Second * 15
	const testNamespace = "default"

	Context("When creating an Environment", func() {
		It("Should transition through appropriate ResourceStatus phases", func() {
			ctx := context.Background()

			// Create test environment
			env := createIntegrationTestEnvironment("test-status-phases", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Environment lookup key
			envLookupKey := types.NamespacedName{
				Name:      env.Name,
				Namespace: testNamespace,
			}

			// Namespace name
			nsName := fmt.Sprintf("%s%s", "test-status-phases", Config.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}

			// Step 1: Verify initial ResourceStatus is set to Creating
			By("Verifying NamespaceStatus is set to Creating when Environment is InProgress")
			Eventually(func() bool {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return false
				}
				if createdEnv.Status.NamespaceStatus == nil {
					return false
				}
				// In very fast test runs, it might already be Active, so accept both
				return createdEnv.Status.NamespaceStatus.Phase == quixiov1.ResourceStatusPhaseCreating ||
					createdEnv.Status.NamespaceStatus.Phase == quixiov1.ResourceStatusPhaseActive
			}, timeout, interval).Should(BeTrue())

			By("Verifying RoleBindingStatus is set correctly")
			Eventually(func() bool {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return false
				}
				if createdEnv.Status.RoleBindingStatus == nil {
					return false
				}
				// Accept either Creating or Active
				return createdEnv.Status.RoleBindingStatus.Phase == quixiov1.ResourceStatusPhaseCreating ||
					createdEnv.Status.RoleBindingStatus.Phase == quixiov1.ResourceStatusPhaseActive
			}, timeout, interval).Should(BeTrue())

			// Step 2: Wait for namespace to be created and verify ResourceStatus transitions to Active
			By("Waiting for namespace to be created")
			Eventually(func() bool {
				createdNs := &corev1.Namespace{}
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying ResourceStatus transitions to Active when Environment is Ready")
			Eventually(func() quixiov1.ResourceStatusPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				if createdEnv.Status.NamespaceStatus == nil {
					return ""
				}
				return createdEnv.Status.NamespaceStatus.Phase
			}, timeout, interval).Should(Equal(quixiov1.ResourceStatusPhaseActive))

			Eventually(func() quixiov1.ResourceStatusPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				if createdEnv.Status.RoleBindingStatus == nil {
					return ""
				}
				return createdEnv.Status.RoleBindingStatus.Phase
			}, timeout, interval).Should(Equal(quixiov1.ResourceStatusPhaseActive))

			// Step 3: Delete the environment and verify ResourceStatus transitions to Deleting
			By("Deleting the environment and verifying ResourceStatus transitions to Deleting")
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			// Allow for transition to Deleting or for environment to be deleted
			Eventually(func() bool {
				createdEnv := &quixiov1.Environment{}
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if errors.IsNotFound(err) {
					// Environment might be deleted before we can check deleting state
					// which is fine for this test
					return true
				}
				if err != nil || createdEnv.Status.NamespaceStatus == nil {
					return false
				}
				return createdEnv.Status.NamespaceStatus.Phase == quixiov1.ResourceStatusPhaseDeleting
			}, timeout, interval).Should(BeTrue(), "Status did not transition to Deleting or environment was not deleted")

			// Verify namespace is deleted or marked for deletion
			Eventually(func() bool {
				ns := &corev1.Namespace{}
				err := k8sClient.Get(ctx, nsLookupKey, ns)
				return IsNamespaceDeleted(ns, err)
			}, deletionTimeout, interval).Should(BeTrue(), "Namespace was not deleted or marked for deletion")
		})
	})

	Context("When namespace creation fails", func() {
		It("Should set the NamespaceStatus to Failed", func() {
			ctx := context.Background()

			// First, create a namespace directly that would conflict with our Environment resource
			conflictId := "test-status-conflict"
			nsName := fmt.Sprintf("%s%s", conflictId, Config.NamespaceSuffix)

			// Create namespace directly with the same name that would be generated for the Environment
			preExistingNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
			}

			err := k8sClient.Create(ctx, preExistingNs)
			Expect(err).To(Not(HaveOccurred()), "Failed to create pre-existing namespace")

			// Create Environment that would try to use the same namespace
			env := createIntegrationTestEnvironment(conflictId, nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Environment lookup key
			envLookupKey := types.NamespacedName{
				Name:      env.Name,
				Namespace: testNamespace,
			}

			// Verify the environment's NamespaceStatus becomes Failed
			By("Verifying NamespaceStatus transitions to Failed when namespace creation fails")
			Eventually(func() quixiov1.ResourceStatusPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				if createdEnv.Status.NamespaceStatus == nil {
					return ""
				}
				return createdEnv.Status.NamespaceStatus.Phase
			}, timeout, interval).Should(Equal(quixiov1.ResourceStatusPhaseFailed))

			// Verify the overall Environment status is Failed
			Eventually(func() quixiov1.EnvironmentPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseFailed))

			// Clean up
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, preExistingNs)).Should(Succeed())
		})
	})

	Context("When role binding creation fails", func() {
		It("Should set the RoleBindingStatus to Failed while keeping NamespaceStatus as Active", func() {
			ctx := context.Background()

			// Save original ClusterRole name for restoration
			originalClusterRole := Config.ClusterRoleName

			// Make sure we reset the ClusterRole name even if the test fails
			defer func() {
				Config.ClusterRoleName = originalClusterRole
			}()

			// Change the ClusterRole to a non-existent one
			Config.ClusterRoleName = "non-existent-role-for-status-test"

			// Create test environment with the invalid ClusterRole
			env := createIntegrationTestEnvironment("test-rb-status-fail", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Environment lookup key
			envLookupKey := types.NamespacedName{
				Name:      env.Name,
				Namespace: testNamespace,
			}

			// Wait for namespace to be created and NamespaceStatus to become Active
			By("Waiting for NamespaceStatus to become Active")
			Eventually(func() quixiov1.ResourceStatusPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				if createdEnv.Status.NamespaceStatus == nil {
					return ""
				}
				return createdEnv.Status.NamespaceStatus.Phase
			}, timeout, interval).Should(Equal(quixiov1.ResourceStatusPhaseActive))

			// Verify RoleBindingStatus becomes Failed
			By("Verifying RoleBindingStatus transitions to Failed when role binding creation fails")
			Eventually(func() quixiov1.ResourceStatusPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				if createdEnv.Status.RoleBindingStatus == nil {
					return ""
				}
				return createdEnv.Status.RoleBindingStatus.Phase
			}, timeout, interval).Should(Equal(quixiov1.ResourceStatusPhaseFailed))

			// Verify the overall Environment status is Failed
			Eventually(func() quixiov1.EnvironmentPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseFailed))

			// Clean up - restore ClusterRole name before deletion to allow proper cleanup
			Config.ClusterRoleName = originalClusterRole

			// Clean up the environment
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			// Verify environment is deleted
			// Use IsNamespaceDeleted helper to check for deletion in envtest
			nsName := fmt.Sprintf("%s%s", "test-rb-status-fail", Config.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}

			Eventually(func() bool {
				ns := &corev1.Namespace{}
				err := k8sClient.Get(ctx, nsLookupKey, ns)
				return IsNamespaceDeleted(ns, err)
			}, deletionTimeout, interval).Should(BeTrue(), "Namespace was not deleted or marked for deletion")
		})
	})

	Context("When a pre-existing namespace is unowned and cannot be deleted", func() {
		It("Should set the NamespaceStatus to Failed during deletion phase", func() {
			ctx := context.Background()

			// First, create a namespace directly
			unownedId := "test-unowned-status"
			nsName := fmt.Sprintf("%s%s", unownedId, Config.NamespaceSuffix)

			// Create namespace without managed-by label
			preExistingNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
					Labels: map[string]string{
						"other-controller": "true",
					},
				},
			}

			err := k8sClient.Create(ctx, preExistingNs)
			Expect(err).To(Not(HaveOccurred()), "Failed to create pre-existing namespace")

			// Create Environment that would try to use the same namespace
			env := createIntegrationTestEnvironment(unownedId, nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Environment lookup key
			envLookupKey := types.NamespacedName{
				Name:      env.Name,
				Namespace: testNamespace,
			}

			// Verify NamespaceStatus becomes Failed
			By("Verifying NamespaceStatus transitions to Failed when namespace is unowned")
			Eventually(func() quixiov1.ResourceStatusPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				if createdEnv.Status.NamespaceStatus == nil {
					return ""
				}
				return createdEnv.Status.NamespaceStatus.Phase
			}, timeout, interval).Should(Equal(quixiov1.ResourceStatusPhaseFailed))

			// Verify the overall Environment status is Failed
			Eventually(func() quixiov1.EnvironmentPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseFailed))

			// Verify NamespaceStatus stays Failed during Environment deletion
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			By("Verifying NamespaceStatus remains Failed during environment deletion")
			Eventually(func() bool {
				createdEnv := &quixiov1.Environment{}
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if errors.IsNotFound(err) {
					// Environment might be deleted before we can check
					return true
				}

				if err != nil || createdEnv.Status.NamespaceStatus == nil {
					return false
				}

				// Validate it's either still Failed or now Deleting, both are acceptable
				return createdEnv.Status.NamespaceStatus.Phase == quixiov1.ResourceStatusPhaseFailed ||
					createdEnv.Status.NamespaceStatus.Phase == quixiov1.ResourceStatusPhaseDeleting
			}, timeout, interval).Should(BeTrue())

			// Cleanup
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, &quixiov1.Environment{})
				return errors.IsNotFound(err)
			}, deletionTimeout, interval).Should(BeTrue(), "Environment was not fully deleted")

			Expect(k8sClient.Delete(ctx, preExistingNs)).Should(Succeed())
		})
	})

	Context("When reconciling with existing ResourceStatus phases", func() {
		It("Should not overwrite more specific ResourceStatus phase information", func() {
			ctx := context.Background()

			// 1. Create test environment
			env := createIntegrationTestEnvironment("test-preserve-status", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Environment lookup key
			envLookupKey := types.NamespacedName{
				Name:      env.Name,
				Namespace: testNamespace,
			}

			// 2. Wait for environment to be Ready with Active ResourceStatus phases
			By("Waiting for environment to be Ready with Active ResourceStatus phases")
			Eventually(func() bool {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return false
				}

				// Check Environment phase is Ready
				if createdEnv.Status.Phase != quixiov1.EnvironmentPhaseReady {
					return false
				}

				// Check ResourceStatus phases are Active
				if createdEnv.Status.NamespaceStatus == nil || createdEnv.Status.RoleBindingStatus == nil {
					return false
				}

				return createdEnv.Status.NamespaceStatus.Phase == quixiov1.ResourceStatusPhaseActive &&
					createdEnv.Status.RoleBindingStatus.Phase == quixiov1.ResourceStatusPhaseActive
			}, timeout, interval).Should(BeTrue())

			// 3. Manually update one ResourceStatus to Failed (simulating partial failure)
			By("Manually setting RoleBindingStatus to Failed")
			modifiedEnv := &quixiov1.Environment{}
			Expect(k8sClient.Get(ctx, envLookupKey, modifiedEnv)).Should(Succeed())

			modifiedEnv.Status.RoleBindingStatus.Phase = quixiov1.ResourceStatusPhaseFailed
			modifiedEnv.Status.RoleBindingStatus.Message = "Manually set to Failed for testing"

			Expect(k8sClient.Status().Update(ctx, modifiedEnv)).Should(Succeed())

			// 4. Update a label to trigger reconciliation while keeping environment in Ready state
			By("Triggering reconciliation by updating labels")
			updatedEnv := &quixiov1.Environment{}
			Expect(k8sClient.Get(ctx, envLookupKey, updatedEnv)).Should(Succeed())

			if updatedEnv.Labels == nil {
				updatedEnv.Labels = make(map[string]string)
			}
			updatedEnv.Labels["test-reconcile"] = "trigger-reconcile"

			Expect(k8sClient.Update(ctx, updatedEnv)).Should(Succeed())

			// 5. Wait for reconciliation to complete and verify Failed status is preserved
			By("Verifying that Failed status is preserved after reconciliation")
			Eventually(func() bool {
				reconciledEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, reconciledEnv); err != nil {
					return false
				}

				// Check labels were updated (reconciliation happened)
				if reconciledEnv.Labels["test-reconcile"] != "trigger-reconcile" {
					return false
				}

				// Check RoleBindingStatus is still Failed
				if reconciledEnv.Status.RoleBindingStatus == nil {
					return false
				}

				return reconciledEnv.Status.RoleBindingStatus.Phase == quixiov1.ResourceStatusPhaseFailed
			}, timeout, interval).Should(BeTrue(), "Failed ResourceStatus was overwritten during reconciliation")

			// Clean up
			Expect(k8sClient.Delete(ctx, updatedEnv)).Should(Succeed())
		})
	})

	Context("When a resource is already in Deleting phase", func() {
		It("Should not overwrite Deleting with Failed phase", func() {
			ctx := context.Background()

			// 1. Create test environment
			env := createIntegrationTestEnvironment("test-deleting-preserved", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Environment lookup key
			envLookupKey := types.NamespacedName{
				Name:      env.Name,
				Namespace: testNamespace,
			}

			// 2. Wait for environment to be Ready
			By("Waiting for environment to be Ready")
			Eventually(func() quixiov1.EnvironmentPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseReady))

			// 3. Manually update status to simulate a resource being deleted
			By("Manually setting NamespaceStatus to Deleting")
			modifiedEnv := &quixiov1.Environment{}
			Expect(k8sClient.Get(ctx, envLookupKey, modifiedEnv)).Should(Succeed())

			modifiedEnv.Status.NamespaceStatus.Phase = quixiov1.ResourceStatusPhaseDeleting
			modifiedEnv.Status.NamespaceStatus.Message = "Deletion in progress"

			Expect(k8sClient.Status().Update(ctx, modifiedEnv)).Should(Succeed())

			// 4. Cause a reconciliation failure that would normally set Failed status
			By("Setting up conditions that would normally set Failed status")

			// Save original namespace name for restoration
			originalNamespaceName := modifiedEnv.Spec.Id

			// Create a temporary namespace with conflicting name that would normally cause a failure
			conflictNsName := fmt.Sprintf("%s%s", originalNamespaceName, Config.NamespaceSuffix)
			conflictNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: conflictNsName,
				},
			}

			// Create conflict namespace (this might fail if the original namespace still exists)
			// But we only need to simulate a condition that would set Failed status
			_ = k8sClient.Create(ctx, conflictNs)

			// 5. Trigger reconciliation and check if Deleting status is preserved
			By("Triggering reconciliation and verifying Deleting status is preserved")
			Eventually(func() quixiov1.ResourceStatusPhase {
				retrievedEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, retrievedEnv); err != nil {
					return ""
				}

				if retrievedEnv.Status.NamespaceStatus == nil {
					return ""
				}

				return retrievedEnv.Status.NamespaceStatus.Phase
			}, timeout, interval).Should(Equal(quixiov1.ResourceStatusPhaseDeleting), "Deleting phase was incorrectly overwritten")

			// Clean up
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())
			k8sClient.Delete(ctx, conflictNs) // Best effort cleanup
		})
	})

	Context("When deleting an Environment", func() {
		It("Should set ResourceStatus phases to Deleting during deletion", func() {
			ctx := context.Background()

			// Create a test environment with a unique name
			env := createIntegrationTestEnvironment("test-deletion-status", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Wait for environment to be Ready
			envLookupKey := types.NamespacedName{
				Name:      env.Name,
				Namespace: testNamespace,
			}

			// Verify it reaches Ready phase
			Eventually(func() quixiov1.EnvironmentPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseReady))

			// Verify ResourceStatus phases are Active
			By("Verifying ResourceStatus phases are Active before deletion")
			// Verify ResourceStatus values are Active
			createdEnv := &quixiov1.Environment{}
			Eventually(func() quixiov1.ResourceStatusPhase {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				if createdEnv.Status.NamespaceStatus == nil {
					return ""
				}
				return createdEnv.Status.NamespaceStatus.Phase
			}, timeout, interval).Should(Equal(quixiov1.ResourceStatusPhaseActive))

			Eventually(func() quixiov1.ResourceStatusPhase {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				if createdEnv.Status.RoleBindingStatus == nil {
					return ""
				}
				return createdEnv.Status.RoleBindingStatus.Phase
			}, timeout, interval).Should(Equal(quixiov1.ResourceStatusPhaseActive))

			// Delete the environment
			Expect(k8sClient.Delete(ctx, createdEnv)).Should(Succeed())

			// Verify ResourceStatus values change to Deleting
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					if errors.IsNotFound(err) {
						// Environment was fully deleted, which is fine for this test
						return true
					}
					return false
				}

				// Check if Environment phase is Deleting
				if createdEnv.Status.Phase != quixiov1.EnvironmentPhaseDeleting {
					return false
				}

				// Check ResourceStatus phases
				if createdEnv.Status.NamespaceStatus == nil || createdEnv.Status.RoleBindingStatus == nil {
					return false
				}

				return createdEnv.Status.NamespaceStatus.Phase == quixiov1.ResourceStatusPhaseDeleting &&
					createdEnv.Status.RoleBindingStatus.Phase == quixiov1.ResourceStatusPhaseDeleting
			}, timeout, interval).Should(BeTrue(), "ResourceStatus phases did not transition to Deleting")
		})
	})
})
