//go:build integration
// +build integration

package environment

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// IsNamespaceDeleted checks if a namespace is deleted by examining either:
// 1. If the namespace retrieval returned a NotFound error
// 2. If the namespace exists but has a DeletionTimestamp set (in the process of being deleted)
func IsNamespaceDeleted(namespace *corev1.Namespace, err error) bool {
	// Case 1: Namespace is not found - already deleted
	if errors.IsNotFound(err) {
		return true
	}

	// Case 2: Namespace is found but has a deletion timestamp
	// For purpose of tests DeletionTimestamp alone is enough also
	// as testing (envtest) will not wait for the namespace to be deleted
	if err == nil && namespace != nil && namespace.DeletionTimestamp != nil {
		return true
	}

	// Namespace exists and is not being deleted
	return false
}

// IsRoleBindingValid checks if a RoleBinding has the expected configuration
func IsRoleBindingValid(rb *rbacv1.RoleBinding, err error, expectedClusterRole, expectedSA, expectedSANamespace string) bool {
	if err != nil {
		return false
	}

	if rb == nil {
		return false
	}

	// Check RoleRef
	if rb.RoleRef.Kind != "ClusterRole" || rb.RoleRef.Name != expectedClusterRole {
		return false
	}

	// Check ServiceAccount subject
	if len(rb.Subjects) == 0 {
		return false
	}

	for _, subj := range rb.Subjects {
		if subj.Kind == "ServiceAccount" &&
			subj.Name == expectedSA &&
			subj.Namespace == expectedSANamespace {
			return true
		}
	}

	return false
}

var _ = Describe("Environment controller integration tests", func() {
	// Increase timeouts to give environment controller more time to reconcile
	const timeout = time.Second * 5
	const interval = time.Millisecond * 250
	const deletionTimeout = time.Second * 10
	const testNamespace = "default"

	// Get the namespace suffix from configuration

	Context("When creating multiple environments simultaneously", func() {
		It("Should handle multiple environments correctly", func() {
			ctx := context.Background()

			// Create three environments
			env1 := createIntegrationTestEnvironment("test-multi-1", nil, nil)
			env2 := createIntegrationTestEnvironment("test-multi-2", nil, nil)
			env3 := createIntegrationTestEnvironment("test-multi-3", nil, nil)

			Expect(k8sClient.Create(ctx, env1)).Should(Succeed())
			Expect(k8sClient.Create(ctx, env2)).Should(Succeed())
			Expect(k8sClient.Create(ctx, env3)).Should(Succeed())

			GinkgoWriter.Printf("Created test environments: %s, %s, %s\n", env1.Name, env2.Name, env3.Name)

			// Expected namespace names
			expectedNames := []string{
				fmt.Sprintf("%s%s", "test-multi-1", Config.NamespaceSuffix),
				fmt.Sprintf("%s%s", "test-multi-2", Config.NamespaceSuffix),
				fmt.Sprintf("%s%s", "test-multi-3", Config.NamespaceSuffix),
			}

			// Check that all namespaces were created
			for _, name := range expectedNames {
				nsLookupKey := types.NamespacedName{Name: name}
				createdNs := &corev1.Namespace{}

				Eventually(func() bool {
					err := k8sClient.Get(ctx, nsLookupKey, createdNs)
					if err != nil {
						GinkgoWriter.Printf("Failed to get namespace %s: %v\n", name, err)
						return false
					}
					GinkgoWriter.Printf("Successfully found namespace %s\n", name)
					return true
				}, timeout, interval).Should(BeTrue(), fmt.Sprintf("Namespace %s was not created in time", name))
			}

			// Check that all environments have Ready status
			for _, envName := range []string{env1.Name, env2.Name, env3.Name} {
				envLookupKey := types.NamespacedName{Name: envName, Namespace: testNamespace}
				createdEnv := &quixiov1.Environment{}

				Eventually(func() quixiov1.EnvironmentPhase {
					if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
						GinkgoWriter.Printf("Failed to get environment %s: %v\n", envName, err)
						return ""
					}
					GinkgoWriter.Printf("Environment %s phase: %s\n", envName, createdEnv.Status.Phase)
					return createdEnv.Status.Phase
				}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseReady))
			}

			// Clean up
			Expect(k8sClient.Delete(ctx, env1)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, env2)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, env3)).Should(Succeed())
		})
	})

	Context("When creating an Environment with custom metadata", func() {
		It("Should apply custom labels and annotations to the namespace", func() {
			ctx := context.Background()

			// Create an environment with custom labels and annotations
			labels := map[string]string{
				"quix.io/custom-label":     "test-value",
				"quix.io/environment-type": "integration-test",
			}

			annotations := map[string]string{
				"quix.io/custom-annotation": "test-value",
				"quix.io/description":       "Integration test environment",
			}

			env := createIntegrationTestEnvironment("test-custom-metadata", labels, annotations)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())
			GinkgoWriter.Printf("Created environment %s\n", env.Name)

			// Namespace name
			nsName := fmt.Sprintf("%s%s", "test-custom-metadata", Config.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}

			// Wait for namespace to be created
			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				if err != nil {
					GinkgoWriter.Printf("Failed to get namespace %s: %v\n", nsName, err)
					return false
				}
				GinkgoWriter.Printf("Successfully found namespace %s\n", nsName)
				return true
			}, timeout, interval).Should(BeTrue(), fmt.Sprintf("Namespace %s was not created in time", nsName))

			// Verify custom labels
			Eventually(func() string {
				updatedNs := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, nsLookupKey, updatedNs); err != nil {
					return ""
				}
				return updatedNs.Labels["quix.io/custom-label"]
			}, timeout, interval).Should(Equal("test-value"))

			Eventually(func() string {
				updatedNs := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, nsLookupKey, updatedNs); err != nil {
					return ""
				}
				return updatedNs.Labels["quix.io/environment-type"]
			}, timeout, interval).Should(Equal("integration-test"))

			// Verify custom annotations
			Eventually(func() string {
				updatedNs := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, nsLookupKey, updatedNs); err != nil {
					return ""
				}
				return updatedNs.Annotations["quix.io/custom-annotation"]
			}, timeout, interval).Should(Equal("test-value"))

			Eventually(func() string {
				updatedNs := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, nsLookupKey, updatedNs); err != nil {
					return ""
				}
				return updatedNs.Annotations["quix.io/description"]
			}, timeout, interval).Should(Equal("Integration test environment"))

			// Clean up
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			// Verify the namespace is deleted
			Eventually(func() bool {
				nsGetErr := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return IsNamespaceDeleted(createdNs, nsGetErr)
			}, deletionTimeout, interval).Should(BeTrue(), "Namespace was not deleted")
		})
	})

	Context("When creating an Environment resource", func() {
		It("Should handle pre-existing namespace conflicts", func() {
			ctx := context.Background()

			// First, create a namespace directly that would conflict with our Environment resource
			conflictId := "test-namespace-conflict"
			nsName := fmt.Sprintf("%s%s", conflictId, Config.NamespaceSuffix)

			// Create namespace directly with the same name that would be generated for the Environment
			preExistingNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
					Labels: map[string]string{
						"pre-existing": "true",
						"preserve-me":  "important-value",
					},
					Annotations: map[string]string{
						"original-annotation": "should-remain-untouched",
					},
				},
			}

			err := k8sClient.Create(ctx, preExistingNs)
			Expect(err).To(Not(HaveOccurred()), "Failed to create pre-existing namespace")

			// Store original namespace state to compare later
			originalNs := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, originalNs)).Should(Succeed())
			originalLabels := make(map[string]string)
			originalAnnotations := make(map[string]string)

			// Deep copy the original labels and annotations
			for k, v := range originalNs.Labels {
				originalLabels[k] = v
			}
			for k, v := range originalNs.Annotations {
				originalAnnotations[k] = v
			}

			// Create Environment that would try to use the same namespace
			env := createIntegrationTestEnvironment(conflictId, nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Lookup the environment and check its status
			envLookupKey := types.NamespacedName{Name: env.Name, Namespace: testNamespace}
			createdEnv := &quixiov1.Environment{}

			// Verify the environment resource enters the Failed state with appropriate error message
			Eventually(func() quixiov1.EnvironmentPhase {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseFailed), "Environment should transition to Failed state on namespace conflict")

			// Verify error message contains details about namespace conflict
			Eventually(func() string {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Message
			}, timeout, interval).Should(ContainSubstring("not managed by this operator"), "Environment should have error message about namespace conflict")

			// Check the pre-existing namespace to see if it was affected
			existingNs := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, existingNs)).Should(Succeed())

			// Verify the namespace was NOT modified - all original labels and annotations should be preserved
			// and no new labels or annotations should be added
			Expect(existingNs.Labels).To(Equal(originalLabels), "Pre-existing namespace labels were modified")
			Expect(existingNs.Annotations).To(Equal(originalAnnotations), "Pre-existing namespace annotations were modified")

			// Specifically check important values remain untouched
			Expect(existingNs.Labels["pre-existing"]).To(Equal("true"), "Original label was modified")
			Expect(existingNs.Labels["preserve-me"]).To(Equal("important-value"), "Important label was modified")
			Expect(existingNs.Annotations["original-annotation"]).To(Equal("should-remain-untouched"), "Original annotation was modified")

			// Verify that the environment-specific labels were NOT added
			_, hasEnvLabel := existingNs.Labels["quix.io/environment-id"]
			Expect(hasEnvLabel).To(BeFalse(), "Operator incorrectly added its labels to pre-existing namespace")

			// Clean up
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, preExistingNs)).Should(Succeed())

			// Wait for namespace deletion confirmation
			Eventually(func() bool {
				nsGetErr := k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, existingNs)
				return IsNamespaceDeleted(existingNs, nsGetErr)
			}, deletionTimeout, interval).Should(BeTrue(), "Pre-existing namespace was not deleted")
		})

		It("Should propagate metadata changes to the namespace", func() {
			ctx := context.Background()

			// Create environment with initial labels and annotations
			initialLabels := map[string]string{"quix.io/initial-label": "initial-value"}
			initialAnnotations := map[string]string{"quix.io/initial-annotation": "initial-value"}

			env := createIntegrationTestEnvironment("test-update-suffix", initialLabels, initialAnnotations)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Namespace name - use the correct suffix from Config
			nsName := fmt.Sprintf("%s%s", env.Spec.Id, Config.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}
			envLookupKey := types.NamespacedName{Name: env.Name, Namespace: testNamespace}

			// Wait for namespace to be created
			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			// Verify initial labels and annotations - use Eventually to wait for controller to set them
			Eventually(func() string {
				updatedNs := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, nsLookupKey, updatedNs); err != nil {
					return ""
				}
				return updatedNs.Labels["quix.io/initial-label"]
			}, timeout, interval).Should(Equal("initial-value"))

			Eventually(func() string {
				updatedNs := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, nsLookupKey, updatedNs); err != nil {
					return ""
				}
				return updatedNs.Annotations["quix.io/initial-annotation"]
			}, timeout, interval).Should(Equal("initial-value"))

			// Wait for environment to be Ready before updating
			createdEnv := &quixiov1.Environment{}
			Eventually(func() quixiov1.EnvironmentPhase {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseReady))

			// Update the environment with new labels and annotations
			updatedEnv := createdEnv.DeepCopy()
			updatedEnv.Spec.Labels = map[string]string{
				"quix.io/initial-label":  "initial-value",
				"quix.io/new-label":      "new-value",
				"quix.io/modified-label": "added-value",
			}
			updatedEnv.Spec.Annotations = map[string]string{
				"quix.io/initial-annotation":  "initial-value",
				"quix.io/new-annotation":      "new-value",
				"quix.io/modified-annotation": "added-value",
			}

			Expect(k8sClient.Update(ctx, updatedEnv)).Should(Succeed())

			// Verify namespace metadata is updated
			Eventually(func() string {
				updatedNs := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, nsLookupKey, updatedNs); err != nil {
					return ""
				}
				return updatedNs.Labels["quix.io/new-label"]
			}, timeout, interval).Should(Equal("new-value"))

			Eventually(func() string {
				updatedNs := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, nsLookupKey, updatedNs); err != nil {
					return ""
				}
				return updatedNs.Annotations["quix.io/new-annotation"]
			}, timeout, interval).Should(Equal("new-value"))

			// Clean up
			Expect(k8sClient.Delete(ctx, updatedEnv)).Should(Succeed())

			// Verify the namespace is deleted
			Eventually(func() bool {
				nsGetErr := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return IsNamespaceDeleted(createdNs, nsGetErr)
			}, deletionTimeout, interval).Should(BeTrue(), "Namespace was not deleted")
		})

		It("Should reject Environment name changes after creation", func() {
			ctx := context.Background()

			// Create a test environment with a unique name
			env := createIntegrationTestEnvironment("test-rb-update", nil, nil)
			originalName := env.Name
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Get the namespace name
			nsName := fmt.Sprintf("%s%s", "test-rb-update", Config.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}

			// Wait for namespace to be created
			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue(), "Namespace was not created")

			// Wait for environment to be Ready
			envLookupKey := types.NamespacedName{
				Name:      originalName,
				Namespace: testNamespace,
			}
			Eventually(func() quixiov1.EnvironmentPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseReady))

			// Check that RoleBinding is created in the namespace
			roleBindingName := fmt.Sprintf("%s-quix-crb", env.Spec.Id)
			rbLookupKey := types.NamespacedName{
				Name:      roleBindingName,
				Namespace: nsName,
			}

			rb := &rbacv1.RoleBinding{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, rbLookupKey, rb)
				return err == nil
			}, timeout, interval).Should(BeTrue(), "RoleBinding was not created")

			// Verify original state
			Expect(rb.RoleRef.Kind).To(Equal("ClusterRole"))
			Expect(rb.RoleRef.Name).To(Equal(Config.ClusterRoleName))
			Expect(rb.Labels["quix.io/environment-name"]).To(Equal(originalName))

			// Scenario 1: Try to modify the same resource by changing just its name
			updatedEnv := &quixiov1.Environment{}
			Expect(k8sClient.Get(ctx, envLookupKey, updatedEnv)).Should(Succeed())

			// Store the original name but modify other field to force update
			updatedEnv.Annotations = map[string]string{"test-annotation": "updated-value"}

			// This update should succeed (no name change)
			Expect(k8sClient.Update(ctx, updatedEnv)).Should(Succeed(), "Update without name change should succeed")

			// Scenario 2: Now try to actually change the name
			updatedEnv.Name = originalName + "-modified"
			err := k8sClient.Update(ctx, updatedEnv)

			// Expect the update to be rejected
			Expect(err).To(HaveOccurred(), "Environment name change should be rejected")
			Expect(err.Error()).To(ContainSubstring("Precondition failed"), "Error should mention precondition failure")

			// Clean up
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			// Verify namespace deletion is initiated
			Eventually(func() bool {
				nsGetErr := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return IsNamespaceDeleted(createdNs, nsGetErr)
			}, deletionTimeout, interval).Should(BeTrue(), "Namespace deletion was not initiated")
		})

		It("Should recreate RoleBinding when configuration changes", func() {
			ctx := context.Background()

			// This test needs to modify Config, so save original values for restoration
			originalClusterRole := Config.ClusterRoleName
			defer func() {
				Config.ClusterRoleName = originalClusterRole
			}()

			// Create a test environment
			env := createIntegrationTestEnvironment("test-rb-config", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Get the namespace name
			nsName := fmt.Sprintf("%s%s", "test-rb-config", Config.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}

			// Wait for namespace to be created
			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue(), "Namespace was not created")

			// Wait for environment to be Ready
			envLookupKey := types.NamespacedName{
				Name:      env.Name,
				Namespace: testNamespace,
			}
			Eventually(func() quixiov1.EnvironmentPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseReady))

			// Get initial RoleBinding
			roleBindingName := fmt.Sprintf("%s-quix-crb", env.Spec.Id)
			rbLookupKey := types.NamespacedName{
				Name:      roleBindingName,
				Namespace: nsName,
			}
			initialRb := &rbacv1.RoleBinding{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, rbLookupKey, initialRb)
				return err == nil
			}, timeout, interval).Should(BeTrue(), "Initial RoleBinding was not created")

			// Verify initial configuration
			Expect(initialRb.RoleRef.Name).To(Equal(Config.ClusterRoleName))

			// Create a new ClusterRole with a different name
			newClusterRoleName := "modified-test-cluster-role"
			newClusterRole := &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name: newClusterRoleName,
				},
				Rules: []rbacv1.PolicyRule{
					{
						APIGroups: []string{""},
						Resources: []string{"pods", "services"},
						Verbs:     []string{"get", "list", "watch"},
					},
				},
			}
			err := k8sClient.Create(ctx, newClusterRole)
			if err != nil && !errors.IsAlreadyExists(err) {
				Fail(fmt.Sprintf("Failed to create new ClusterRole: %v", err))
			}

			// Change the operator configuration to use the new ClusterRole
			Config.ClusterRoleName = newClusterRoleName

			// Update environment to force reconciliation
			updatedEnv := &quixiov1.Environment{}
			Expect(k8sClient.Get(ctx, envLookupKey, updatedEnv)).Should(Succeed())

			By("forcing reconciliation by triggering it directly with the updated config")
			// Best would be to restart the controller with the new config but this is easier
			// to avoid having two controllers running at the same time

			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      updatedEnv.Name,
					Namespace: updatedEnv.Namespace,
				},
			}
			_, err = Controller.Reconcile(ctx, req)
			Expect(err).ToNot(HaveOccurred(), "Failed to reconcile with new config")

			// Verify that the RoleBinding was recreated with the new ClusterRole
			Eventually(func() string {
				rb := &rbacv1.RoleBinding{}
				if err := k8sClient.Get(ctx, rbLookupKey, rb); err != nil {
					return ""
				}
				return rb.RoleRef.Name
			}, timeout, interval).Should(Equal(newClusterRoleName), "RoleBinding was not updated with new ClusterRole")

			// Get the recreated RoleBinding and verify its configuration is correct
			recreatedRb := &rbacv1.RoleBinding{}
			Expect(k8sClient.Get(ctx, rbLookupKey, recreatedRb)).Should(Succeed())

			// Verify the role reference directly instead of using timestamps
			Expect(recreatedRb.RoleRef.Name).To(Equal(newClusterRoleName), "RoleBinding has incorrect ClusterRole")
			Expect(recreatedRb.RoleRef.Kind).To(Equal("ClusterRole"), "RoleRef kind should be ClusterRole")
			Expect(recreatedRb.RoleRef.APIGroup).To(Equal("rbac.authorization.k8s.io"), "RoleRef API group is incorrect")

			// Verify the subjects are correctly set
			Expect(recreatedRb.Subjects).To(HaveLen(1), "RoleBinding should have exactly one subject")
			Expect(recreatedRb.Subjects[0].Kind).To(Equal("ServiceAccount"), "Subject should be a ServiceAccount")
			Expect(recreatedRb.Subjects[0].Name).To(Equal(Config.ServiceAccountName), "ServiceAccount name is incorrect")
			Expect(recreatedRb.Subjects[0].Namespace).To(Equal(Config.ServiceAccountNamespace), "ServiceAccount namespace is incorrect")

			// Clean up
			Expect(k8sClient.Delete(ctx, updatedEnv)).Should(Succeed())

			// Attempt to clean up the new ClusterRole (best effort)
			k8sClient.Delete(ctx, newClusterRole)
		})

		It("Should update RoleBinding when ServiceAccount changes", func() {
			ctx := context.Background()

			// Save original service account name for restoration
			originalServiceAccountName := Config.ServiceAccountName
			defer func() {
				Config.ServiceAccountName = originalServiceAccountName
			}()

			// Create a test environment
			env := createIntegrationTestEnvironment("test-rb-sa", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Get the namespace name
			nsName := fmt.Sprintf("%s%s", "test-rb-sa", Config.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}

			// Wait for namespace to be created
			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue(), "Namespace was not created")

			// Wait for environment to be Ready
			envLookupKey := types.NamespacedName{
				Name:      env.Name,
				Namespace: testNamespace,
			}
			Eventually(func() quixiov1.EnvironmentPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseReady))

			// Check that initial RoleBinding is created
			roleBindingName := fmt.Sprintf("%s-quix-crb", env.Spec.Id)
			rbLookupKey := types.NamespacedName{
				Name:      roleBindingName,
				Namespace: nsName,
			}

			// Wait for role binding to be created and verify initial configuration
			initialRb := &rbacv1.RoleBinding{}
			Eventually(func() error {
				return k8sClient.Get(ctx, rbLookupKey, initialRb)
			}, timeout, interval).Should(Succeed(), "Initial RoleBinding was not created")

			// Verify initial service account
			Expect(initialRb.Subjects).To(HaveLen(1), "Initial RoleBinding should have one subject")
			Expect(initialRb.Subjects[0].Kind).To(Equal("ServiceAccount"), "Subject kind should be ServiceAccount")
			Expect(initialRb.Subjects[0].Name).To(Equal(Config.ServiceAccountName), "Unexpected ServiceAccount name")
			Expect(initialRb.Subjects[0].Namespace).To(Equal(Config.ServiceAccountNamespace), "Unexpected ServiceAccount namespace")

			// Create a new ServiceAccount with a different name
			newServiceAccountName := "modified-test-service-account"
			newServiceAccount := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      newServiceAccountName,
					Namespace: Config.ServiceAccountNamespace,
				},
			}
			err := k8sClient.Create(ctx, newServiceAccount)
			if err != nil && !errors.IsAlreadyExists(err) {
				Fail(fmt.Sprintf("Failed to create new ServiceAccount: %v", err))
			}

			// Change the operator configuration to use the new ServiceAccount
			Config.ServiceAccountName = newServiceAccountName

			// Update environment to force reconciliation
			updatedEnv := &quixiov1.Environment{}
			Expect(k8sClient.Get(ctx, envLookupKey, updatedEnv)).Should(Succeed())

			// Force reconciliation by triggering it directly
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      updatedEnv.Name,
					Namespace: updatedEnv.Namespace,
				},
			}
			_, err = Controller.Reconcile(ctx, req)
			Expect(err).ToNot(HaveOccurred(), "Failed to reconcile with new ServiceAccount")

			// Verify that the RoleBinding was updated with the new ServiceAccount
			Eventually(func() string {
				rb := &rbacv1.RoleBinding{}
				if err := k8sClient.Get(ctx, rbLookupKey, rb); err != nil {
					return ""
				}
				if len(rb.Subjects) == 0 {
					return ""
				}
				return rb.Subjects[0].Name
			}, timeout, interval).Should(Equal(newServiceAccountName), "RoleBinding was not updated with new ServiceAccount")

			// Get the updated RoleBinding and verify its configuration
			updatedRb := &rbacv1.RoleBinding{}
			Expect(k8sClient.Get(ctx, rbLookupKey, updatedRb)).Should(Succeed())

			// Verify the ServiceAccount was updated correctly
			Expect(updatedRb.Subjects).To(HaveLen(1), "Updated RoleBinding should have one subject")
			Expect(updatedRb.Subjects[0].Kind).To(Equal("ServiceAccount"), "Subject kind should be ServiceAccount")
			Expect(updatedRb.Subjects[0].Name).To(Equal(newServiceAccountName), "ServiceAccount name not updated")
			Expect(updatedRb.Subjects[0].Namespace).To(Equal(Config.ServiceAccountNamespace), "ServiceAccount namespace is incorrect")

			// Clean up
			Expect(k8sClient.Delete(ctx, updatedEnv)).Should(Succeed())

			// Attempt to clean up the new ServiceAccount (best effort)
			k8sClient.Delete(ctx, newServiceAccount)
		})
	})

	Context("When deleting an Environment resource", func() {
		It("Should go through the Deleting phase", func() {
			ctx := context.Background()

			// Create a test environment
			env := createIntegrationTestEnvironment("test-delete-phases", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Namespace name
			nsName := fmt.Sprintf("%s%s", "test-delete-phases", Config.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}

			// Wait for namespace to be created
			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			// Look up the environment resource
			envLookupKey := types.NamespacedName{
				Name:      env.Name,
				Namespace: testNamespace,
			}
			createdEnv := &quixiov1.Environment{}

			// Wait for environment to be Ready
			Eventually(func() quixiov1.EnvironmentPhase {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseReady))

			// Delete the environment
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			// Verify it goes through the Deleting phase
			Eventually(func() quixiov1.EnvironmentPhase {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					if errors.IsNotFound(err) {
						// Environment was fully deleted, which is fine for this test
						return quixiov1.EnvironmentPhaseDeleting
					}
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseDeleting))

			// We don't wait for actual deletion - that's not the focus of this test
		})

		It("Should initiate namespace deletion during environment deletion", func() {
			ctx := context.Background()

			// Create a test environment with a unique name
			env := createIntegrationTestEnvironment("test-events", nil, nil)
			envName := env.Name
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Get the namespace name
			nsName := fmt.Sprintf("%s%s", "test-events", Config.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}

			// Wait for environment to be Ready
			envLookupKey := types.NamespacedName{
				Name:      envName,
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

			// Verify namespace was created
			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue(), "Namespace was not created")

			// Delete the environment
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			// Check that the environment enters Deleting phase
			Eventually(func() quixiov1.EnvironmentPhase {
				createdEnv := &quixiov1.Environment{}
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					if errors.IsNotFound(err) {
						// Environment was fully deleted, which is fine for this test
						return quixiov1.EnvironmentPhaseDeleting
					}
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseDeleting), "Environment not in Deleting phase")

			// Check that namespace deletion is initiated (namespace has DeletionTimestamp)
			Eventually(func() bool {
				nsGetErr := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return IsNamespaceDeleted(createdNs, nsGetErr)
			}, timeout, interval).Should(BeTrue(), "Namespace deletion was not initiated")
		})

		It("Should create a RoleBinding in the managed namespace", func() {
			ctx := context.Background()

			// Create a test environment
			env := createIntegrationTestEnvironment("test-rbac", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Get the namespace name
			nsName := fmt.Sprintf("%s%s", "test-rbac", Config.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}

			// Wait for namespace to be created
			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue(), "Namespace was not created")

			// Check that the environment reaches Ready phase
			envLookupKey := types.NamespacedName{
				Name:      env.Name,
				Namespace: testNamespace,
			}
			Eventually(func() quixiov1.EnvironmentPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseReady))

			// Check that RoleBinding is created in the namespace
			roleBindingName := fmt.Sprintf("%s-quix-crb", env.Spec.Id)
			rbLookupKey := types.NamespacedName{
				Name:      roleBindingName,
				Namespace: nsName,
			}

			// Check that the RoleBinding exists and has correct configuration
			rb := &rbacv1.RoleBinding{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, rbLookupKey, rb)
				return IsRoleBindingValid(rb, err, Config.ClusterRoleName,
					Config.ServiceAccountName,
					Config.ServiceAccountNamespace)
			}, timeout, interval).Should(BeTrue(), "RoleBinding not correctly configured")

			// Verify RoleBinding labels
			Expect(rb.Labels["quix.io/environment-id"]).To(Equal(env.Spec.Id), "Environment ID label is incorrect")
			Expect(rb.Labels["quix.io/managed-by"]).To(Equal("environment-operator"), "Managed-by label is incorrect")
			Expect(rb.Labels["quix.io/environment-name"]).To(Equal(env.Name), "Environment name label is incorrect")

			// Clean up
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			// Verify namespace deletion is initiated
			Eventually(func() bool {
				nsGetErr := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return IsNamespaceDeleted(createdNs, nsGetErr)
			}, deletionTimeout, interval).Should(BeTrue(), "Namespace deletion was not initiated")
		})
	})

	Context("When creating Environments with invalid metadata", func() {
		It("Should reject or filter labels/annotations without quix.io/ prefix", func() {
			ctx := context.Background()

			// Create an environment with custom labels that don't use the required prefix
			invalidLabels := map[string]string{
				"invalid-label":               "test-value",
				"kubernetes.io/metadata.name": "attempted-override",
				"quix.io/valid-label":         "this-should-remain",
			}

			invalidAnnotations := map[string]string{
				"invalid-annotation":        "test-value",
				"kubernetes.io/description": "attempted-override",
				"quix.io/valid-annotation":  "this-should-remain",
			}

			env := createIntegrationTestEnvironment("test-invalid-metadata", invalidLabels, invalidAnnotations)

			// API validation may reject this, but our controller also filters out these values
			// So test for either outcome
			err := k8sClient.Create(ctx, env)

			// If environment was created, verify the invalid labels/annotations are filtered out
			if err == nil {
				nsName := fmt.Sprintf("%s%s", "test-invalid-metadata", Config.NamespaceSuffix)
				nsLookupKey := types.NamespacedName{Name: nsName}

				// Wait for namespace to be created
				createdNs := &corev1.Namespace{}
				Eventually(func() bool {
					err := k8sClient.Get(ctx, nsLookupKey, createdNs)
					return err == nil
				}, timeout, interval).Should(BeTrue(), "Namespace was not created")

				// Valid prefixed labels should be kept
				Expect(createdNs.Labels).To(HaveKey("quix.io/valid-label"))
				Expect(createdNs.Labels["quix.io/valid-label"]).To(Equal("this-should-remain"))

				// Invalid non-prefixed should be filtered
				Expect(createdNs.Labels).NotTo(HaveKey("invalid-label"))
				Expect(createdNs.Labels).NotTo(HaveKey("kubernetes.io/metadata.name"))

				// Valid prefixed annotations should be kept
				Expect(createdNs.Annotations).To(HaveKey("quix.io/valid-annotation"))
				Expect(createdNs.Annotations["quix.io/valid-annotation"]).To(Equal("this-should-remain"))

				// Invalid non-prefixed should be filtered
				Expect(createdNs.Annotations).NotTo(HaveKey("invalid-annotation"))
				Expect(createdNs.Annotations).NotTo(HaveKey("kubernetes.io/description"))

				// Clean up
				Expect(k8sClient.Delete(ctx, env)).Should(Succeed())
			} else {
				// If the API validation rejected it, verify it was due to validation errors
				GinkgoWriter.Printf("Environment creation was rejected with error: %v\n", err)
				Expect(err.Error()).To(Or(
					ContainSubstring("prefix"),
					ContainSubstring("valid"),
					ContainSubstring("metadata"),
				))
			}
		})

		It("Should prevent overriding controller-managed labels", func() {
			ctx := context.Background()

			// Try to override protected labels
			conflictingLabels := map[string]string{
				"quix.io/managed-by":       "malicious-controller",
				"quix.io/environment-id":   "different-id",
				"quix.io/environment-name": "wrong-name",
			}

			env := createIntegrationTestEnvironment("test-metadata-override", conflictingLabels, nil)

			// API validation may reject this, but our controller also filters out these values
			// So test for either outcome
			err := k8sClient.Create(ctx, env)

			// If environment was created, verify the protected labels were not overridden
			if err == nil {
				nsName := fmt.Sprintf("%s%s", "test-metadata-override", Config.NamespaceSuffix)
				nsLookupKey := types.NamespacedName{Name: nsName}

				// Wait for namespace to be created
				createdNs := &corev1.Namespace{}
				Eventually(func() bool {
					err := k8sClient.Get(ctx, nsLookupKey, createdNs)
					return err == nil
				}, timeout, interval).Should(BeTrue(), "Namespace was not created")

				// Verify protected labels have the correct controller-managed values
				Expect(createdNs.Labels["quix.io/managed-by"]).To(Equal("quix-environment-operator"))
				Expect(createdNs.Labels["quix.io/environment-id"]).To(Equal("test-metadata-override"))
				Expect(createdNs.Labels["quix.io/environment-name"]).To(Equal(env.Name))

				// Clean up
				Expect(k8sClient.Delete(ctx, env)).Should(Succeed())
			} else {
				// If the API validation rejected it, verify it was due to validation errors
				GinkgoWriter.Printf("Environment creation was rejected with error: %v\n", err)
				Expect(err.Error()).To(Or(
					ContainSubstring("protected"),
					ContainSubstring("override"),
					ContainSubstring("managed"),
				))
			}
		})

		It("Should enforce metadata validation during updates", func() {
			ctx := context.Background()

			// First create a valid environment with proper prefixed labels
			validLabels := map[string]string{
				"quix.io/valid-label": "initial-value",
			}

			validAnnotations := map[string]string{
				"quix.io/valid-annotation": "initial-value",
			}

			env := createIntegrationTestEnvironment("test-update-validation", validLabels, validAnnotations)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Get the namespace name
			nsName := fmt.Sprintf("%s%s", "test-update-validation", Config.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}

			// Wait for namespace to be created
			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue(), "Namespace was not created")

			// Verify initial valid label is applied
			Expect(createdNs.Labels).To(HaveKey("quix.io/valid-label"))
			Expect(createdNs.Labels["quix.io/valid-label"]).To(Equal("initial-value"))

			// Wait for environment to be Ready
			envLookupKey := types.NamespacedName{
				Name:      env.Name,
				Namespace: testNamespace,
			}
			createdEnv := &quixiov1.Environment{}
			Eventually(func() quixiov1.EnvironmentPhase {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseReady))

			// Now try to update with invalid labels and attempt to override protected labels
			updatedEnv := createdEnv.DeepCopy()
			updatedEnv.Spec.Labels = map[string]string{
				"quix.io/valid-label":      "updated-value",       // Valid update
				"invalid-label":            "non-prefixed-value",  // Invalid - no prefix
				"quix.io/managed-by":       "malicious-override",  // Protected label
				"quix.io/environment-id":   "attempt-id-change",   // Protected label
				"quix.io/environment-name": "attempt-name-change", // Protected label
			}

			updatedEnv.Spec.Annotations = map[string]string{
				"quix.io/valid-annotation": "updated-value",      // Valid update
				"random-annotation":        "non-prefixed-value", // Invalid - no prefix
				"quix.io/created-by":       "malicious-override", // Protected annotation
			}

			// API validation may reject this, but our controller also filters values
			// So test for either outcome
			err := k8sClient.Update(ctx, updatedEnv)

			if err == nil {
				// If update was allowed, wait for reconciliation and check filtering
				Eventually(func() string {
					ns := &corev1.Namespace{}
					if err := k8sClient.Get(ctx, nsLookupKey, ns); err != nil {
						return ""
					}
					return ns.Labels["quix.io/valid-label"]
				}, timeout, interval).Should(Equal("updated-value"), "Valid label update was not applied")

				// Check that the namespace was correctly updated with valid labels and
				// protected labels were not overridden
				updatedNs := &corev1.Namespace{}
				Expect(k8sClient.Get(ctx, nsLookupKey, updatedNs)).Should(Succeed())

				// Valid updates should be applied
				Expect(updatedNs.Labels["quix.io/valid-label"]).To(Equal("updated-value"))
				Expect(updatedNs.Annotations["quix.io/valid-annotation"]).To(Equal("updated-value"))

				// Invalid non-prefixed values should be filtered
				Expect(updatedNs.Labels).NotTo(HaveKey("invalid-label"))
				Expect(updatedNs.Annotations).NotTo(HaveKey("random-annotation"))

				// Protected values should not be overridden
				Expect(updatedNs.Labels["quix.io/managed-by"]).To(Equal("quix-environment-operator"))
				Expect(updatedNs.Labels["quix.io/environment-id"]).To(Equal("test-update-validation"))
				Expect(updatedNs.Labels["quix.io/environment-name"]).To(Equal(env.Name))
				Expect(updatedNs.Annotations["quix.io/created-by"]).To(Equal("quix-environment-operator"))
			} else {
				// If the API validation rejected the update, verify the rejection reason
				GinkgoWriter.Printf("Environment update was rejected with error: %v\n", err)
				Expect(err.Error()).To(Or(
					ContainSubstring("prefix"),
					ContainSubstring("protected"),
					ContainSubstring("override"),
				))
			}

			// Clean up
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())
		})

		It("Should handle invalid environment IDs according to configuration", func() {
			ctx := context.Background()

			// Save the original regex pattern for restoration
			originalRegex := Config.EnvironmentRegex
			defer func() {
				Config.EnvironmentRegex = originalRegex
			}()

			// Set a regex pattern that requires lowercase letters only
			Config.EnvironmentRegex = "^[a-z]+$"

			// Create an environment with an invalid ID (contains numbers, which doesn't match the regex)
			env := createIntegrationTestEnvironment("test123-invalid", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Get the environment lookup key
			envLookupKey := types.NamespacedName{
				Name:      env.Name,
				Namespace: testNamespace,
			}

			// Verify the environment transitions to Failed status
			Eventually(func() quixiov1.EnvironmentPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseFailed))

			// Verify the error message contains information about invalid ID
			Eventually(func() string {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Message
			}, timeout, interval).Should(ContainSubstring("invalid environment ID"))

			// Verify no namespace was created
			nsName := fmt.Sprintf("%s%s", "test123-invalid", Config.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}
			createdNs := &corev1.Namespace{}

			Consistently(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "Namespace should not be created for invalid environment ID")

			// Clean up
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())
		})
	})
})

func createIntegrationTestEnvironment(id string, labels, annotations map[string]string) *quixiov1.Environment {
	return &quixiov1.Environment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "quix.io/v1",
			Kind:       "Environment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      id + "-resource-name",
			Namespace: "default",
		},
		Spec: quixiov1.EnvironmentSpec{
			Id:          id,
			Labels:      labels,
			Annotations: annotations,
		},
	}
}
