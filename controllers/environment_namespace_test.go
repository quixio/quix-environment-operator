package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/status"
)

// Note: Setup (BeforeSuite, AfterSuite) and helper functions (createTestEnvironment)
// remain in environment_controller_test.go for now. Consider refactoring helpers later if needed.

var _ = Describe("Environment controller - Namespace Management", func() {
	const timeout = time.Second * 10
	const interval = time.Millisecond * 250
	const testNamespace = "default" // Assuming test Env CRDs are created in default

	Context("When creating an Environment resource", func() {
		It("Should create a namespace with the correct name and properly handle deletion", func() {
			By("Creating a new Environment")
			ctx := context.Background()
			envID := "test-ns-lifecycle"
			env := &quixiov1.Environment{
				TypeMeta: metav1.TypeMeta{APIVersion: "quix.io/v1", Kind: "Environment"},
				ObjectMeta: metav1.ObjectMeta{
					Name:      envID + "-resource",
					Namespace: testNamespace,
				},
				Spec: quixiov1.EnvironmentSpec{Id: envID},
			}
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			envLookupKey := types.NamespacedName{Name: env.Name, Namespace: testNamespace}
			createdEnv := &quixiov1.Environment{}

			Eventually(func() bool {
				return k8sClient.Get(ctx, envLookupKey, createdEnv) == nil
			}, timeout, interval).Should(BeTrue())

			// Determine expected namespace name based on reconciler config
			expectedNsName := fmt.Sprintf("%s%s", env.Spec.Id, testConfig.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: expectedNsName}
			createdNs := &corev1.Namespace{}

			By("Verifying Namespace creation")
			Eventually(func() bool {
				return k8sClient.Get(ctx, nsLookupKey, createdNs) == nil
			}, timeout, interval).Should(BeTrue())

			// Verify namespace metadata applied by reconciler/manager
			Expect(createdNs.Labels[ManagedByLabel]).To(Equal(OperatorName))
			Expect(createdNs.Labels[LabelEnvironmentID]).To(Equal(env.Spec.Id))
			Expect(createdNs.Annotations[AnnotationEnvironmentCRDNamespace]).To(Equal(env.Namespace))

			By("Verifying Environment status reflects Namespace name")
			Eventually(func() string {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Namespace
			}, timeout, interval).Should(Equal(expectedNsName))

			// Check RoleBinding creation as part of happy path (could be moved to RB test later)
			By("Verifying RoleBinding creation")
			rbLookupKey := types.NamespacedName{Name: testConfig.GetRoleBindingName(), Namespace: expectedNsName}
			createdRb := &rbacv1.RoleBinding{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, rbLookupKey, createdRb)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying Environment reaches Ready phase")
			Eventually(func() quixiov1.EnvironmentPhase {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady))

			By("Deleting the Environment resource")
			Expect(k8sClient.Delete(ctx, createdEnv)).Should(Succeed()) // Use createdEnv which has UID etc.

			By("Verifying Environment transitions to PhaseDeleting")
			Eventually(func() bool {
				env := &quixiov1.Environment{}
				err := k8sClient.Get(ctx, envLookupKey, env)
				// If it's gone, that's success in the context of deletion completing
				if errors.IsNotFound(err) {
					return true
				}
				if err != nil {
					return false // Other error getting the Env
				}
				// Check if deletion timestamp is set or phase is Deleting
				return !env.DeletionTimestamp.IsZero() || env.Status.Phase == quixiov1.PhaseDeleting
			}, timeout, interval).Should(BeTrue(), "Environment should be marked for deletion or enter Deleting phase")

			By("Waiting for the Namespace to be deleted")
			Eventually(func() bool {
				ns := &corev1.Namespace{}
				err := k8sClient.Get(ctx, nsLookupKey, ns)
				if errors.IsNotFound(err) {
					return true // Namespace is gone
				}
				if err == nil && !ns.DeletionTimestamp.IsZero() {
					return true // Namespace is marked for deletion
				}
				// Return false for other errors or if namespace exists without deletion timestamp
				return false
			}, timeout, interval).Should(BeTrue(), "Namespace associated with the Environment was not deleted or marked for deletion")

			By("Waiting for the Environment resource to be fully deleted")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				return errors.IsNotFound(err) // Environment should eventually be gone
			}, timeout, interval).Should(BeTrue(), "Environment resource was not fully deleted")

		})

		// TODO: Add tests for namespace collision (existing unmanaged namespace)
		// TODO: Add tests for namespace update (e.g. labels/annotations change on Env)
		// TODO: Add tests for failures during namespace creation
	})

	Context("When Environment Spec generates invalid Namespace name", func() {
		It("Should fail if generated namespace name is too long", func() {
			By("Creating an Environment with an ID that will generate a namespace name > 63 chars")
			ctx := context.Background()

			// Assuming NamespaceSuffix = "-suffix", max ID length is 63 - len("-suffix") = 56
			// Let's use an ID slightly longer than allowed by typical webhook/CRD validation
			// A common limit for spec fields might be lower than the generated name limit.
			// The error message indicated a limit of 44 for spec.id.
			longID := strings.Repeat("a", 45) // Exceeds the observed spec.id limit of 44

			invalidEnv := &quixiov1.Environment{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "quix.io/v1",
					Kind:       "Environment",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "too-long-id",
					Namespace: "default",
				},
				Spec: quixiov1.EnvironmentSpec{
					Id: longID,
				},
			}

			By("Expecting creation to fail due to validation")
			err := k8sClient.Create(ctx, invalidEnv)
			Expect(err).Should(HaveOccurred(), "Environment CR creation with too long ID should fail")
			Expect(errors.IsInvalid(err)).Should(BeTrue(), "Error should be an invalid API object error")
			// Optionally, check the error message content
			Expect(err.Error()).Should(ContainSubstring("spec.id: Too long"))
		})
	})

	Context("When updating an Environment resource", func() {
		It("Should propagate label and annotation changes to the namespace", func() {
			By("Creating a new Environment")
			ctx := context.Background()
			env := createTestEnvironment("test-update", map[string]string{"initial": "value"}, map[string]string{"init-anno": "value"})
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			expectedNsName := fmt.Sprintf("%s%s", env.Spec.Id, testConfig.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: expectedNsName}

			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			Expect(createdNs.Labels["initial"]).To(Equal("value"))
			Expect(createdNs.Annotations["init-anno"]).To(Equal("value"))

			envLookupKey := types.NamespacedName{Name: env.Name, Namespace: testNamespace}
			createdEnv := &quixiov1.Environment{}

			Eventually(func() quixiov1.EnvironmentPhase {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady))

			updatedEnv := &quixiov1.Environment{}
			Expect(k8sClient.Get(ctx, envLookupKey, updatedEnv)).Should(Succeed())

			updatedEnv.Spec.Labels = map[string]string{
				"initial":   "value",
				"new-label": "new-value",
			}
			updatedEnv.Spec.Annotations = map[string]string{
				"init-anno": "value",
				"new-anno":  "new-value",
			}

			Expect(k8sClient.Update(ctx, updatedEnv)).Should(Succeed())

			By("Verifying updated labels on Namespace")
			Eventually(func() string {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				if err != nil {
					return ""
				}
				return createdNs.Labels["new-label"]
			}, timeout, interval).Should(Equal("new-value"))

			By("Verifying updated annotations on Namespace")
			Eventually(func() string {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				if err != nil {
					return ""
				}
				return createdNs.Annotations["new-anno"]
			}, timeout, interval).Should(Equal("new-value"))

			// Also check that the operator's own labels/annotations are preserved
			Expect(createdNs.Labels[ManagedByLabel]).To(Equal(OperatorName))
			Expect(createdNs.Labels[LabelEnvironmentID]).To(Equal(env.Spec.Id))
			Expect(createdNs.Annotations[AnnotationEnvironmentCRDNamespace]).To(Equal(env.Namespace))

			Eventually(func() quixiov1.EnvironmentPhase {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady))

			By("Deleting the Environment")
			Expect(k8sClient.Delete(ctx, updatedEnv)).Should(Succeed())
		})

		// TODO: Add test for removing labels/annotations
		// TODO: Add test for conflicting labels/annotations (e.g., trying to set managed-by)
	})

	Context("When updating an Environment resource fails due to invalid metadata", func() {
		It("Should transition to UpdateFailed phase when namespace labels/annotations are invalid", func() {
			By("Creating a new Environment")
			ctx := context.Background()

			env := createTestEnvironment("valid-name-fail-update", map[string]string{"initial": "value"}, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			expectedNsName := fmt.Sprintf("%s%s", env.Spec.Id, testConfig.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: expectedNsName}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, &corev1.Namespace{})
				return err == nil
			}, timeout, interval).Should(BeTrue(), "Namespace should be created")

			envLookupKey := types.NamespacedName{Name: env.Name, Namespace: testNamespace}

			Eventually(func() quixiov1.EnvironmentPhase {
				updatedEnv := &quixiov1.Environment{}
				err := k8sClient.Get(ctx, envLookupKey, updatedEnv)
				if err != nil {
					return ""
				}
				return updatedEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady), "Environment should become Ready initially")

			By("Updating with extremely long labels that would exceed Kubernetes label length limits")
			createdEnv := &quixiov1.Environment{}
			Expect(k8sClient.Get(ctx, envLookupKey, createdEnv)).Should(Succeed())

			// Create label data that's too long for Kubernetes (labels have a 63 character limit)
			veryLongValue := strings.Repeat("x", 70)

			// Update the environment with labels that are too long
			createdEnv.Spec.Labels = map[string]string{
				"valid-key": veryLongValue,
			}

			Expect(k8sClient.Update(ctx, createdEnv)).Should(Succeed(), "Update to CR should succeed")

			By("Verifying environment phase changes to UpdateFailed")
			Eventually(func() quixiov1.EnvironmentPhase {
				failedEnv := &quixiov1.Environment{}
				err := k8sClient.Get(ctx, envLookupKey, failedEnv)
				if err != nil {
					return ""
				}
				return failedEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseUpdateFailed), "Environment should transition to UpdateFailed")

			By("Verifying error message is present")
			failedEnv := &quixiov1.Environment{}
			Expect(k8sClient.Get(ctx, envLookupKey, failedEnv)).Should(Succeed())
			Expect(failedEnv.Status.ErrorMessage).NotTo(BeEmpty(), "Error message should be set")
			// Check for a specific error related to metadata update failure
			Expect(failedEnv.Status.ErrorMessage).To(ContainSubstring("metadata.labels: Invalid value"))

			By("Verifying the Ready condition is False")
			var readyCondition *metav1.Condition
			readyCondition = meta.FindStatusCondition(failedEnv.Status.Conditions, status.ConditionTypeReady)
			Expect(readyCondition).NotTo(BeNil(), "Ready condition should exist")
			Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse), "Ready condition should be False")

			By("Fixing the environment to verify recovery")
			fixedEnv := &quixiov1.Environment{}
			Expect(k8sClient.Get(ctx, envLookupKey, fixedEnv)).Should(Succeed())
			fixedEnv.Spec.Labels = map[string]string{
				"valid-key": "valid-value",
			}
			Expect(k8sClient.Update(ctx, fixedEnv)).Should(Succeed())

			By("Verifying environment returns to Ready phase after fix")
			Eventually(func() quixiov1.EnvironmentPhase {
				recoveredEnv := &quixiov1.Environment{}
				err := k8sClient.Get(ctx, envLookupKey, recoveredEnv)
				if err != nil {
					return ""
				}
				return recoveredEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady), "Environment should return to Ready state")

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, fixedEnv)).Should(Succeed())
		})
	})

	Context("When creating an Environment resource with metadata", func() {
		It("Should apply custom labels and annotations to the namespace", func() {
			By("Creating an Environment with custom labels and annotations")
			ctx := context.Background()

			labels := map[string]string{
				"custom-label":     "test-value",
				"environment-type": "test",
			}

			annotations := map[string]string{
				"custom-annotation": "test-value",
				"description":       "Test environment",
			}

			env := createTestEnvironment("test-custom-meta", labels, annotations)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			expectedNsName := fmt.Sprintf("%s%s", env.Spec.Id, testConfig.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: expectedNsName}
			createdNs := &corev1.Namespace{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			Expect(createdNs.Labels["custom-label"]).To(Equal("test-value"))
			Expect(createdNs.Labels["environment-type"]).To(Equal("test"))
			Expect(createdNs.Annotations["custom-annotation"]).To(Equal("test-value"))
			Expect(createdNs.Annotations["description"]).To(Equal("Test environment"))

			// Ensure operator's own labels/annotations are also present
			Expect(createdNs.Labels[ManagedByLabel]).To(Equal(OperatorName))
			Expect(createdNs.Labels[LabelEnvironmentID]).To(Equal(env.Spec.Id))
			Expect(createdNs.Annotations[AnnotationEnvironmentCRDNamespace]).To(Equal(env.Namespace))

			By("Deleting the Environment")
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())
		})
	})

	Context("When a namespace with the target name already exists", func() {
		It("Should fail Environment creation and not modify the existing namespace", func() {
			By("Creating a namespace manually")
			ctx := context.Background()
			envID := "test-collision"
			expectedNsName := fmt.Sprintf("%s%s", envID, testConfig.NamespaceSuffix)
			preExistingNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: expectedNsName,
					Labels: map[string]string{
						"pre-existing": "true", // Label to identify this namespace
					},
				},
			}
			Expect(k8sClient.Create(ctx, preExistingNs)).Should(Succeed(), "Failed to create pre-existing namespace")

			// Ensure namespace is created before proceeding
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: expectedNsName}, &corev1.Namespace{})
				return err == nil
			}, timeout, interval).Should(BeTrue(), "Pre-existing namespace not found")

			By("Creating an Environment resource targeting the existing namespace name")
			env := createTestEnvironment(envID, nil, nil)
			env.Name = "collision-test-env" // Ensure unique Environment CR name
			Expect(k8sClient.Create(ctx, env)).Should(Succeed(), "Failed to create Environment CR")

			envLookupKey := types.NamespacedName{Name: env.Name, Namespace: testNamespace}
			createdEnv := &quixiov1.Environment{}

			By("Verifying the Environment enters CreateFailed phase")
			Eventually(func() quixiov1.EnvironmentPhase {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseCreateFailed), "Environment phase should be CreateFailed")

			By("Verifying the Environment has an error message")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return false
				}
				return strings.Contains(createdEnv.Status.ErrorMessage, "already exists and is not managed by this operator")
			}, timeout, interval).Should(BeTrue(), "Expected error message about existing unmanaged namespace")

			By("Verifying the Ready condition is False")
			Eventually(func() metav1.ConditionStatus {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return metav1.ConditionUnknown
				}
				for _, cond := range createdEnv.Status.Conditions {
					if cond.Type == status.ConditionTypeReady {
						return cond.Status
					}
				}
				return metav1.ConditionUnknown
			}, timeout, interval).Should(Equal(metav1.ConditionFalse), "Ready condition should be False")

			By("Verifying the pre-existing namespace was not modified")
			retrievedNs := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: expectedNsName}, retrievedNs)).Should(Succeed(), "Failed to retrieve namespace after Environment creation attempt")
			Expect(retrievedNs.Labels).To(HaveKeyWithValue("pre-existing", "true"), "Namespace should retain its original labels")
			Expect(retrievedNs.Labels).NotTo(HaveKey("quix.io/managed-by"), "Namespace should not have operator labels added")
			Expect(retrievedNs.DeletionTimestamp).To(BeNil(), "Namespace should not be marked for deletion")

			By("Deleting the Environment CR")
			Expect(k8sClient.Delete(ctx, createdEnv)).Should(Succeed(), "Failed to delete Environment CR")

			By("Ensuring the Environment CR is deleted")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "Environment CR was not deleted")

			By("Verifying the pre-existing namespace still exists and was not deleted")
			Consistently(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: expectedNsName}, retrievedNs)
			}, time.Second*2, interval).Should(Succeed(), "Namespace should consistently exist after Environment CR deletion")
			Expect(retrievedNs.DeletionTimestamp).To(BeNil(), "Namespace should still not be marked for deletion")

			By("Cleaning up the pre-existing namespace")
			Expect(k8sClient.Delete(ctx, preExistingNs)).Should(Succeed(), "Failed to delete pre-existing namespace during cleanup")

			// Check namespace is deleted or marked for deletion (for envtest compatibility)
			Eventually(func() bool {
				nsGetErr := k8sClient.Get(ctx, types.NamespacedName{Name: expectedNsName}, preExistingNs)
				// Use proper namespace deletion check that also considers DeletionTimestamp
				// Note: Using the actual NamespaceManager logic here would be better if possible
				// For envtest, checking IsNotFound or DeletionTimestamp is usually sufficient.
				return errors.IsNotFound(nsGetErr) || !preExistingNs.DeletionTimestamp.IsZero()
			}, timeout, interval).Should(BeTrue(), "Namespace was not deleted or marked for deletion in time")
		})
	})

	Context("When validating Environment label inputs for Namespace", func() {
		It("Should reject environments with invalid label formats", func() {
			By("Creating an Environment with invalid label format")
			ctx := context.Background()

			// Use a label with invalid characters that should definitely be rejected
			env := createTestEnvironment("valid-id-invalid-labels", map[string]string{
				"invalid@label!key": "value", // Special characters not allowed in label keys
			}, nil)

			Expect(k8sClient.Create(ctx, env)).Should(Succeed(), "Environment CR creation should succeed")

			envLookupKey := types.NamespacedName{Name: env.Name, Namespace: testNamespace}
			createdEnv := &quixiov1.Environment{}

			By("Verifying it enters failed state or has error message")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return false
				}

				// Check for CreateFailed phase OR presence of error message
				return createdEnv.Status.Phase == quixiov1.PhaseCreateFailed ||
					len(createdEnv.Status.ErrorMessage) > 0
			}, timeout, interval).Should(BeTrue(), "Environment should enter CreateFailed state or have error message")

			By("Verifying error message eventually appears")
			Eventually(func() string {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return ""
				}
				return createdEnv.Status.ErrorMessage
			}, timeout, interval).ShouldNot(BeEmpty(), "Error message should be set")

			By("Verifying namespace was not created")
			expectedNsName := fmt.Sprintf("%s%s", env.Spec.Id, testConfig.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: expectedNsName}

			Consistently(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, &corev1.Namespace{})
				return errors.IsNotFound(err)
			}, time.Second*2, interval).Should(BeTrue(), "Namespace should not be created")

			By("Verifying Status conditions accurately reflect the failure")
			Eventually(func() metav1.ConditionStatus {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return metav1.ConditionUnknown
				}
				for _, cond := range createdEnv.Status.Conditions {
					if cond.Type == status.ConditionTypeReady {
						return cond.Status
					}
				}
				return metav1.ConditionUnknown
			}, timeout, interval).Should(Equal(metav1.ConditionFalse), "Ready condition should be False")

			By("Fixing the environment labels")
			fixedEnv := createdEnv.DeepCopy()
			fixedEnv.Spec.Labels = map[string]string{
				"valid-label-key": "value",
			}

			Expect(k8sClient.Update(ctx, fixedEnv)).Should(Succeed(), "Update with fixed labels should succeed")

			By("Verifying environment transitions to Ready after fix")
			Eventually(func() quixiov1.EnvironmentPhase {
				updatedEnv := &quixiov1.Environment{}
				err := k8sClient.Get(ctx, envLookupKey, updatedEnv)
				if err != nil {
					return ""
				}
				return updatedEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady), "Environment should become Ready after fix")

			By("Verifying namespace is now created")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, &corev1.Namespace{})
				return err == nil
			}, timeout, interval).Should(BeTrue(), "Namespace should be created after fix")

			By("Cleaning up")
			recoveredEnv := &quixiov1.Environment{}
			Expect(k8sClient.Get(ctx, envLookupKey, recoveredEnv)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, recoveredEnv)).Should(Succeed())
		})
	})

})
