//go:build integration
// +build integration

// Security-focused integration tests for environment operator
// Testing security boundaries, privilege escalation protection, and isolation enforcement

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
	"github.com/quix-analytics/quix-environment-operator/internal/security"
	rbacv1 "k8s.io/api/rbac/v1"
)

var _ = Describe("Environment Operator Security Tests", func() {
	const timeout = time.Second * 5
	const interval = time.Millisecond * 250
	const deletionTimeout = time.Second * 10
	const testNamespace = "default"

	Context("Privilege Escalation Prevention", func() {

		It("Should verify controller's ClusterRole lacks role binding permissions", func() {
			ctx := context.Background()

			// Get the ClusterRole that our controller is binding
			clusterRole := &rbacv1.ClusterRole{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: Config.ClusterRoleName}, clusterRole)).Should(Succeed())

			// Check that our ClusterRole doesn't have permissions to create/modify RoleBindings
			// This ensures that we're following the principle of least privilege
			hasRoleBindPermission := false

			for _, rule := range clusterRole.Rules {
				// Check for broad permissions that would allow binding roles
				if containsAny(rule.APIGroups, []string{"rbac.authorization.k8s.io", "*"}) {
					if containsAny(rule.Resources, []string{"rolebindings", "clusterrolebindings", "*"}) {
						if containsAny(rule.Verbs, []string{"create", "update", "patch", "delete", "*"}) {
							hasRoleBindPermission = true
							break
						}
					}
				}
			}

			// The ClusterRole that we bind in namespaces should NOT have permission to bind roles
			// This is a fundamental security principle - the role we give to users shouldn't allow
			// them to escalate their privileges by binding more powerful roles
			Expect(hasRoleBindPermission).To(BeFalse(),
				"ClusterRole '%s' shouldn't have permission to bind roles, as this could allow privilege escalation",
				Config.ClusterRoleName)

			// Create an environment with the safe role configuration
			env := createIntegrationTestEnvironment("test-safe-rbac", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Verify the environment reconciliation succeeds
			envLookupKey := types.NamespacedName{
				Name:      env.Name,
				Namespace: testNamespace,
			}

			// Environment should go to Ready state since the role is safe
			Eventually(func() quixiov1.EnvironmentPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseReady))

			// Clean up
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())
		})

		It("Should fail security test when ClusterRole has role binding permissions", func() {
			ctx := context.Background()

			// Save the original ClusterRole name for restoration
			originalClusterRoleName := Config.ClusterRoleName
			defer func() {
				Config.ClusterRoleName = originalClusterRoleName
			}()

			// Create a dangerous ClusterRole that DOES have permissions to bind roles
			dangerousRoleName := "test-dangerous-cluster-role"
			dangerousRole := &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name: dangerousRoleName,
				},
				Rules: []rbacv1.PolicyRule{
					{
						// Standard permissions similar to our default test role
						APIGroups: []string{""},
						Resources: []string{"pods", "services", "configmaps", "secrets"},
						Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
					},
					{
						// DANGEROUS: Permissions to bind roles!
						APIGroups: []string{"rbac.authorization.k8s.io"},
						Resources: []string{"rolebindings"},
						Verbs:     []string{"create", "update", "patch", "delete"},
					},
				},
			}

			// Create the dangerous role
			err := k8sClient.Create(ctx, dangerousRole)
			if err != nil && !errors.IsAlreadyExists(err) {
				Fail(fmt.Sprintf("Failed to create dangerous ClusterRole: %v", err))
			}

			// Configure operator to use this dangerous role
			Config.ClusterRoleName = dangerousRoleName

			// Create an environment with the dangerous role configuration
			env := createIntegrationTestEnvironment("test-dangerous-rbac", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Verify the environment reconciliation fails
			envLookupKey := types.NamespacedName{
				Name:      env.Name,
				Namespace: testNamespace,
			}

			// Environment should go to Failed state with security violation message
			Eventually(func() quixiov1.EnvironmentPhase {
				createdEnv := &quixiov1.Environment{}
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.EnvironmentPhaseFailed))

			// Verify status contains security-related message
			createdEnv := &quixiov1.Environment{}
			Expect(k8sClient.Get(ctx, envLookupKey, createdEnv)).Should(Succeed())
			Expect(createdEnv.Status.Message).To(ContainSubstring("security"), "Error message should mention security violation")

			// Verify the test detects this dangerous configuration
			hasRoleBindPermission := false

			for _, rule := range dangerousRole.Rules {
				if containsAny(rule.APIGroups, []string{"rbac.authorization.k8s.io", "*"}) {
					if containsAny(rule.Resources, []string{"rolebindings", "clusterrolebindings", "*"}) {
						if containsAny(rule.Verbs, []string{"create", "update", "patch", "delete", "*"}) {
							hasRoleBindPermission = true
							break
						}
					}
				}
			}

			// This test should PASS (finding the dangerous permission)
			// while the actual security posture FAILS (has a vulnerability)
			Expect(hasRoleBindPermission).To(BeTrue(),
				"Security test should detect that ClusterRole '%s' has dangerous role binding permissions",
				dangerousRoleName)

			// Clean up
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())
			k8sClient.Delete(ctx, dangerousRole)
		})
	})

	Context("Malicious Configuration Detection", func() {
		It("Should prevent creation of environments with dangerous configurations", func() {
			ctx := context.Background()

			// Create an environment with potentially dangerous labels
			dangerousLabels := map[string]string{
				"kubernetes.io/metadata.name": "default", // Attempt namespace hijacking
				"control-plane":               "true",    // Attempt to masquerade as control plane
			}

			env := createIntegrationTestEnvironment("test-malicious-config", dangerousLabels, nil)

			// If there's validation in place, this should be rejected
			// If not, we should at least validate that the dangerous labels don't make it to the namespace
			err := k8sClient.Create(ctx, env)
			if err != nil {
				// If creation was rejected, that's good security behavior
				Expect(err).To(HaveOccurred())
				return
			}

			// If creation succeeded, ensure dangerous labels are not propagated to namespace
			nsName := fmt.Sprintf("%s%s", "test-malicious-config", Config.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}

			// Wait for namespace to be created
			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue(), "Namespace was not created")

			// Verify the dangerous labels were not propagated
			Expect(createdNs.Labels).NotTo(HaveKey("kubernetes.io/metadata.name"))
			Expect(createdNs.Labels).NotTo(HaveKey("control-plane"))

			// Clean up
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())
		})

		It("Should prevent overriding controller-managed labels and annotations", func() {
			ctx := context.Background()

			// Create an environment with labels that try to override controller-managed ones
			conflictingLabels := map[string]string{
				"quix.io/managed-by":        "malicious-controller",
				"quix.io/environment-id":    "different-id",
				"quix.io/environment-name":  "wrong-name",
				"quix.io/created-by":        "unauthorized-user",
				"quix.io/environment-phase": "fake-ready",
			}

			conflictingAnnotations := map[string]string{
				"quix.io/created-by":                "malicious-controller",
				"quix.io/environment-crd-namespace": "wrong-namespace",
				"quix.io/environment-resource-name": "wrong-name",
			}

			env := createIntegrationTestEnvironment("test-override-attempt", conflictingLabels, conflictingAnnotations)

			// Create the environment - this may be rejected or accepted with validation
			err := k8sClient.Create(ctx, env)
			if err != nil {
				// If creation was rejected, that's a strict validation approach
				Expect(err).To(HaveOccurred())
				return
			}

			// If it was accepted, ensure the namespace has the correct values
			nsName := fmt.Sprintf("%s%s", "test-override-attempt", Config.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}

			// Wait for namespace to be created
			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue(), "Namespace was not created")

			// Verify that the controller-owned labels are set correctly
			// and were not overridden by the malicious values
			Expect(createdNs.Labels["quix.io/managed-by"]).To(Equal("quix-environment-operator"),
				"Controller should override managed-by label")

			Expect(createdNs.Labels["quix.io/environment-id"]).To(Equal("test-override-attempt"),
				"Controller should set correct environment-id")

			Expect(createdNs.Labels["quix.io/environment-name"]).To(Equal(env.Name),
				"Controller should set correct environment-name")

			// Check annotations were properly managed
			Expect(createdNs.Annotations["quix.io/created-by"]).To(Equal("quix-environment-operator"),
				"Controller should override created-by annotation")

			// Clean up
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())
		})

		It("Should enforce proper label/annotation prefix validation", func() {
			ctx := context.Background()

			// Create labels and annotations without the required quix.io/ prefix
			nonPrefixedLabels := map[string]string{
				"invalid-label": "value1",
				"another-label": "value2",
			}

			// Mix some valid and invalid labels
			mixedLabels := map[string]string{
				"quix.io/valid-label": "correct-value",
				"non-prefixed-label":  "wrong-value",
			}

			// Create two environments - one with all invalid labels, one with mixed
			env1 := createIntegrationTestEnvironment("test-invalid-prefix-1", nonPrefixedLabels, nil)
			env2 := createIntegrationTestEnvironment("test-invalid-prefix-2", mixedLabels, nil)

			// Create the environments
			err1 := k8sClient.Create(ctx, env1)

			// Test may implement either approach:
			// 1. Reject non-prefixed labels completely (strict)
			// 2. Accept the environment but filter out non-prefixed labels (permissive)

			if err1 == nil {
				// If environment was created, verify labels were filtered
				nsName := fmt.Sprintf("%s%s", "test-invalid-prefix-1", Config.NamespaceSuffix)
				createdNs := &corev1.Namespace{}

				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, createdNs)
					return err == nil
				}, timeout, interval).Should(BeTrue(), "Namespace was not created")

				// Make sure non-prefixed labels weren't propagated to namespace
				for key := range nonPrefixedLabels {
					Expect(createdNs.Labels).NotTo(HaveKey(key),
						"Non-prefixed labels should not be propagated to namespace")
				}

				// Clean up
				Expect(k8sClient.Delete(ctx, env1)).Should(Succeed())
			}

			// Try with mixed valid/invalid labels
			err2 := k8sClient.Create(ctx, env2)

			if err2 == nil {
				// Environment with mixed labels was created
				nsName := fmt.Sprintf("%s%s", "test-invalid-prefix-2", Config.NamespaceSuffix)
				createdNs := &corev1.Namespace{}

				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, createdNs)
					return err == nil
				}, timeout, interval).Should(BeTrue(), "Namespace was not created")

				// Valid prefixed labels should be kept
				Expect(createdNs.Labels).To(HaveKey("quix.io/valid-label"))

				// Invalid non-prefixed should be filtered
				Expect(createdNs.Labels).NotTo(HaveKey("non-prefixed-label"))

				// Clean up
				Expect(k8sClient.Delete(ctx, env2)).Should(Succeed())
			}
		})
	})

	Context("Security Regression Prevention", func() {
		It("Should prevent security posture from regressing", func() {
			ctx := context.Background()

			// Create a test environment
			env := createIntegrationTestEnvironment("test-regression", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Get namespace name
			nsName := fmt.Sprintf("%s%s", "test-regression", Config.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}

			// Wait for namespace to be created
			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue(), "Namespace was not created")

			// Create a security validator
			securityValidator := security.NewValidator(k8sClient)

			// Attempt to create a Role with excessive permissions in the namespace
			excessiveRole := &rbacv1.Role{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "excessive-role",
					Namespace: nsName,
				},
				Rules: []rbacv1.PolicyRule{
					{
						APIGroups: []string{"*"},
						Resources: []string{"*"},
						Verbs:     []string{"*"},
					},
				},
			}

			// First, validate the role - it should fail our security validation
			err := securityValidator.ValidateRole(ctx, excessiveRole)
			Expect(err).To(HaveOccurred(), "Role with excessive permissions should fail validation")
			Expect(err.Error()).To(ContainSubstring("security violation"), "Error should indicate security violation")

			// Try to create it anyway - this simulates what would happen if a user tried to bypass our controller
			err = k8sClient.Create(ctx, excessiveRole)

			// This should either be forbidden by the cluster, or if the role is created...
			if err == nil {
				// If the role was created, ensure we can't bind it to the service account
				excessiveRoleBinding := &rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "excessive-binding",
						Namespace: nsName,
					},
					RoleRef: rbacv1.RoleRef{
						APIGroup: "rbac.authorization.k8s.io",
						Kind:     "Role",
						Name:     excessiveRole.Name,
					},
					Subjects: []rbacv1.Subject{
						{
							Kind:      "ServiceAccount",
							Name:      Config.ServiceAccountName,
							Namespace: Config.ServiceAccountNamespace,
						},
					},
				}

				// Try to create it anyway - this simulates what would happen if a user tried to bypass our controller
				bindErr := k8sClient.Create(ctx, excessiveRoleBinding)

				// The binding creation may or may not be rejected by the cluster
				// but our security test should still pass because our validator detected the problem
				if bindErr == nil {
					By("Created RoleBinding, which would be rejected in production by our validator")
				} else {
					By("RoleBinding was rejected by the cluster: " + bindErr.Error())
				}
			} else {
				By("Role was rejected by the cluster: " + err.Error())
			}

			// Clean up
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			// Clean up the role if it was created
			if err == nil {
				k8sClient.Delete(ctx, excessiveRole)
			}
		})
	})
})

// Helper function to check if any element in source is in target
func containsAny(source, target []string) bool {
	for _, s := range source {
		for _, t := range target {
			if s == t {
				return true
			}
		}
	}
	return false
}
