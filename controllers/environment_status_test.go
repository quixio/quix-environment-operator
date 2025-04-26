package controllers

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/status"
)

var _ = Describe("Environment controller - Status Management", func() {
	const timeout = time.Second * 10
	const interval = time.Millisecond * 250
	const testNamespace = "default"

	Context("When checking environment conditions", func() {
		It("Should set all required conditions during lifecycle", func() {
			By("Creating a new Environment")
			ctx := context.Background()
			env := createTestEnvironment("test-conditions", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			envLookupKey := types.NamespacedName{Name: env.Name, Namespace: testNamespace}
			createdEnv := &quixiov1.Environment{}

			// Wait for the environment to become Ready (Phase)
			Eventually(func() quixiov1.EnvironmentPhase {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return "" // Return empty string on error
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady), "Environment should reach PhaseReady")

			By("Verifying all sub-phases eventually become Ready")

			// Check NamespacePhase
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, envLookupKey, createdEnv)).Should(Succeed())
				g.Expect(createdEnv.Status.NamespacePhase).To(Equal(string(quixiov1.PhaseStateReady)))
			}, timeout, interval).Should(Succeed(), "NamespacePhase should become Ready")

			// Check RoleBindingPhase
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, envLookupKey, createdEnv)).Should(Succeed())
				g.Expect(createdEnv.Status.RoleBindingPhase).To(Equal(string(quixiov1.PhaseStateReady)))
			}, timeout, interval).Should(Succeed(), "RoleBindingPhase should become Ready")

			By("Verifying the Ready condition is eventually True")
			checkCondition := func(env *quixiov1.Environment, conditionType string) metav1.ConditionStatus {
				for _, condition := range env.Status.Conditions {
					if condition.Type == conditionType {
						return condition.Status
					}
				}
				return metav1.ConditionUnknown // Condition not found
			}

			// Wait specifically for Ready condition
			Eventually(func(g Gomega) {
				// Must re-get the env inside Eventually for latest status
				currentEnv := &quixiov1.Environment{}
				g.Expect(k8sClient.Get(ctx, envLookupKey, currentEnv)).Should(Succeed())
				g.Expect(checkCondition(currentEnv, status.ConditionTypeReady)).To(Equal(metav1.ConditionTrue))
			}, timeout, interval).Should(Succeed(), "Ready condition should become True")

			By("Deleting the Environment")
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			// Check that the Ready condition becomes False upon deletion
			Eventually(func() bool {
				currentEnv := &quixiov1.Environment{}
				err := k8sClient.Get(ctx, envLookupKey, currentEnv)
				if errors.IsNotFound(err) {
					return true // Environment was fully deleted, counts as Ready=False implicitly
				}

				if err != nil {
					// Log transient errors but don't fail the check immediately
					logf.Log.Error(err, "Error getting environment during deletion check")
					return false
				}

				// Environment still exists, check the Ready condition
				found := false
				for _, condition := range currentEnv.Status.Conditions {
					if condition.Type == status.ConditionTypeReady {
						found = true
						if condition.Status != metav1.ConditionFalse {
							return false // Ready condition exists but is not False
						}
						break // Found the Ready condition
					}
				}
				// If the Ready condition is not found, it might have been removed (which is ok in deleting state)
				// If it is found, it must be False.
				return !found || checkCondition(currentEnv, status.ConditionTypeReady) == metav1.ConditionFalse

			}, timeout, interval).Should(BeTrue(), "Ready condition should become False or be removed after deletion starts")
		})
	})

	// TODO: Add more tests for specific status transitions, e.g., UpdateFailed, error messages, condition reasons.
})
