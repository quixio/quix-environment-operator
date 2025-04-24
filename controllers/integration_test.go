//go:build integration
// +build integration

// Integration tests for complete system behavior with real component interactions.
// High-level verification of end-to-end functionality.

package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
)

var _ = Describe("Environment controller integration tests", func() {
	const timeout = time.Second * 15
	const interval = time.Millisecond * 250
	const deletionTimeout = time.Second * 30
	const testNamespace = "default"

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

			// Expected namespace names
			expectedNames := []string{
				fmt.Sprintf("%s%s", "test-multi-1", testConfig.NamespaceSuffix),
				fmt.Sprintf("%s%s", "test-multi-2", testConfig.NamespaceSuffix),
				fmt.Sprintf("%s%s", "test-multi-3", testConfig.NamespaceSuffix),
			}

			// Check that all namespaces were created
			for _, name := range expectedNames {
				nsLookupKey := types.NamespacedName{Name: name}
				createdNs := &corev1.Namespace{}

				Eventually(func() bool {
					err := k8sClient.Get(ctx, nsLookupKey, createdNs)
					return err == nil
				}, timeout, interval).Should(BeTrue())
			}

			// Check that all environments have Ready status
			for _, envName := range []string{env1.Name, env2.Name, env3.Name} {
				envLookupKey := types.NamespacedName{Name: envName, Namespace: testNamespace}
				createdEnv := &quixiov1.Environment{}

				Eventually(func() quixiov1.EnvironmentPhase {
					if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
						return ""
					}
					return createdEnv.Status.Phase
				}, timeout, interval).Should(Equal(quixiov1.PhaseReady))
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
				"custom-label":     "test-value",
				"environment-type": "integration-test",
			}

			annotations := map[string]string{
				"custom-annotation": "test-value",
				"description":       "Integration test environment",
			}

			env := createIntegrationTestEnvironment("test-custom-metadata", labels, annotations)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Namespace name
			nsName := fmt.Sprintf("%s%s", "test-custom-metadata", testConfig.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: nsName}

			// Wait for namespace to be created
			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			// Verify custom labels
			Eventually(func() string {
				updatedNs := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, nsLookupKey, updatedNs); err != nil {
					return ""
				}
				return updatedNs.Labels["custom-label"]
			}, timeout, interval).Should(Equal("test-value"))

			Eventually(func() string {
				updatedNs := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, nsLookupKey, updatedNs); err != nil {
					return ""
				}
				return updatedNs.Labels["environment-type"]
			}, timeout, interval).Should(Equal("integration-test"))

			// Verify custom annotations
			Eventually(func() string {
				updatedNs := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, nsLookupKey, updatedNs); err != nil {
					return ""
				}
				return updatedNs.Annotations["custom-annotation"]
			}, timeout, interval).Should(Equal("test-value"))

			Eventually(func() string {
				updatedNs := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, nsLookupKey, updatedNs); err != nil {
					return ""
				}
				return updatedNs.Annotations["description"]
			}, timeout, interval).Should(Equal("Integration test environment"))

			// Clean up
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			// Verify the namespace is deleted
			Eventually(func() bool {
				nsGetErr := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return mockNamespaceManager.IsNamespaceDeleted(createdNs, nsGetErr)
			}, deletionTimeout, interval).Should(BeTrue(), "Namespace was not considered deleted by the manager in time")
		})
	})

	Context("When updating an Environment resource", func() {
		It("Should propagate metadata changes to the namespace", func() {
			ctx := context.Background()

			// Create environment with initial labels and annotations
			initialLabels := map[string]string{"initial-label": "initial-value"}
			initialAnnotations := map[string]string{"initial-annotation": "initial-value"}

			env := createIntegrationTestEnvironment("test-update-suffix", initialLabels, initialAnnotations)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Namespace name - use the correct suffix from testConfig
			nsName := fmt.Sprintf("%s%s", env.Spec.Id, testConfig.NamespaceSuffix)
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
				return updatedNs.Labels["initial-label"]
			}, timeout, interval).Should(Equal("initial-value"))

			Eventually(func() string {
				updatedNs := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, nsLookupKey, updatedNs); err != nil {
					return ""
				}
				return updatedNs.Annotations["initial-annotation"]
			}, timeout, interval).Should(Equal("initial-value"))

			// Wait for environment to be Ready before updating
			createdEnv := &quixiov1.Environment{}
			Eventually(func() quixiov1.EnvironmentPhase {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady))

			// Update the environment with new labels and annotations
			updatedEnv := createdEnv.DeepCopy()
			updatedEnv.Spec.Labels = map[string]string{
				"initial-label":  "initial-value",
				"new-label":      "new-value",
				"modified-label": "added-value",
			}
			updatedEnv.Spec.Annotations = map[string]string{
				"initial-annotation":  "initial-value",
				"new-annotation":      "new-value",
				"modified-annotation": "added-value",
			}

			Expect(k8sClient.Update(ctx, updatedEnv)).Should(Succeed())

			// Verify namespace metadata is updated
			Eventually(func() string {
				updatedNs := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, nsLookupKey, updatedNs); err != nil {
					return ""
				}
				return updatedNs.Labels["new-label"]
			}, timeout, interval).Should(Equal("new-value"))

			Eventually(func() string {
				updatedNs := &corev1.Namespace{}
				if err := k8sClient.Get(ctx, nsLookupKey, updatedNs); err != nil {
					return ""
				}
				return updatedNs.Annotations["new-annotation"]
			}, timeout, interval).Should(Equal("new-value"))

			// Clean up
			Expect(k8sClient.Delete(ctx, updatedEnv)).Should(Succeed())

			// Verify the namespace is deleted
			Eventually(func() bool {
				nsGetErr := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return mockNamespaceManager.IsNamespaceDeleted(createdNs, nsGetErr)
			}, deletionTimeout, interval).Should(BeTrue(), "Namespace was not considered deleted by the manager in time")
		})
	})

	Context("When deleting an Environment resource", func() {
		It("Should go through the Deleting phase", func() {
			ctx := context.Background()

			// Create a test environment
			env := createIntegrationTestEnvironment("test-delete-phases", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Namespace name
			nsName := fmt.Sprintf("%s%s", "test-delete-phases", testConfig.NamespaceSuffix)
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
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady))

			// Delete the environment
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			// Verify it goes through the Deleting phase
			Eventually(func() quixiov1.EnvironmentPhase {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					// Might be already gone, which is fine
					return quixiov1.PhaseDeleting
				}
				return createdEnv.Status.Phase
			}, deletionTimeout, interval).Should(Equal(quixiov1.PhaseDeleting))

			// Wait for the environment resource to be fully deleted
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				return err != nil // Error means it's gone
			}, deletionTimeout, interval).Should(BeTrue(), "Environment was not deleted within timeout")

			// Verify the namespace is also deleted
			Eventually(func() bool {
				nsGetErr := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return mockNamespaceManager.IsNamespaceDeleted(createdNs, nsGetErr)
			}, deletionTimeout, interval).Should(BeTrue(), "Namespace was not considered deleted by the manager in time")
		})

		It("Should raise the NamespaceDeleted event during environment deletion", func() {
			ctx := context.Background()

			// Create a test environment with a unique name
			env := createIntegrationTestEnvironment("test-events", nil, nil)
			envName := env.Name
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

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
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady))

			// Delete the environment
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			// Wait and check for an event with "NamespaceDeleted" reason
			// This checks for controller events in a more integration-friendly way
			Eventually(func() bool {
				// List events related to this resource
				eventList := &eventsv1.EventList{}
				if err := k8sClient.List(ctx, eventList); err != nil {
					return false
				}

				// Check each event for the NamespaceDeleted event
				for _, event := range eventList.Items {
					if event.Reason == "NamespaceDeleted" &&
						strings.Contains(event.Regarding.Name, envName) {
						return true
					}
				}
				return false
			}, deletionTimeout, interval).Should(BeTrue(), "NamespaceDeleted event was not raised")
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
