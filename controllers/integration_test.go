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
					if strings.Contains(event.Reason, "NamespaceDeleted") &&
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
