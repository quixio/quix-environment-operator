// Functional tests against a simulated Kubernetes cluster using envtest.
// Verifies controller's behavior as a complete unit without external dependencies.

package controllers

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment
var ctx context.Context
var cancel context.CancelFunc
var testConfig *config.OperatorConfig

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = quixiov1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).ToNot(HaveOccurred())

	// Set up the controller with a test configuration
	testConfig = &config.OperatorConfig{
		NamespaceSuffix:         "-suffix",
		ServiceAccountName:      "test-service-account",
		ServiceAccountNamespace: "default",
		ClusterRoleName:         "test-cluster-role",
	}

	err = (&EnvironmentReconciler{
		Client:   k8sManager.GetClient(),
		Scheme:   k8sManager.GetScheme(),
		Recorder: k8sManager.GetEventRecorderFor("environment-controller"),
		Config:   testConfig,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(ctx)
		Expect(err).ToNot(HaveOccurred(), "failed to run manager")
	}()
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

// Helper functions
func createTestEnvironment(id string, labels, annotations map[string]string) *quixiov1.Environment {
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

// Test cases
var _ = Describe("Environment controller", func() {
	const timeout = time.Second * 10
	const interval = time.Millisecond * 250
	const testNamespace = "default"

	Context("When creating an Environment resource", func() {
		It("Should create a namespace with the correct name", func() {
			By("Creating a new Environment")
			ctx := context.Background()
			env := createTestEnvironment("test", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Look up the environment resource after creation
			envLookupKey := types.NamespacedName{Name: env.Name, Namespace: testNamespace}
			createdEnv := &quixiov1.Environment{}

			// Verify the environment was created
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					GinkgoWriter.Printf("Failed to get Environment: %v\n", err)
					return false
				}
				GinkgoWriter.Printf("Retrieved Environment: name=%s, phase=%s, namespace=%s, resourceVersion=%s\n",
					createdEnv.Spec.Id,
					createdEnv.Status.Phase,
					createdEnv.Status.Namespace,
					createdEnv.ResourceVersion)
				return true
			}, timeout, interval).Should(BeTrue())

			// Expected namespace name
			expectedNsName := fmt.Sprintf("%s%s", env.Spec.Id, testConfig.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: expectedNsName}
			createdNs := &corev1.Namespace{}

			GinkgoWriter.Printf("Looking for namespace with name: %s\n", expectedNsName)

			// List all namespaces to debug
			var nsList corev1.NamespaceList
			Expect(k8sClient.List(ctx, &nsList)).Should(Succeed())
			GinkgoWriter.Printf("Found %d namespaces in the cluster:\n", len(nsList.Items))
			for _, ns := range nsList.Items {
				GinkgoWriter.Printf("  - %s (labels: %v)\n", ns.Name, ns.Labels)
			}

			// Verify the namespace was created (with extra logging)
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				if err != nil {
					GinkgoWriter.Printf("Failed to get namespace %s: %v\n", expectedNsName, err)
					return false
				}
				GinkgoWriter.Printf("Retrieved namespace %s successfully (UID: %s, labels: %v)\n",
					createdNs.Name, createdNs.UID, createdNs.Labels)
				return true
			}, timeout, interval).Should(BeTrue())

			// Verify namespace labels
			Expect(createdNs.Labels["quix.io/managed-by"]).To(Equal("quix-environment-operator"))
			Expect(createdNs.Labels["quix.io/environment-id"]).To(Equal(env.Spec.Id))

			// Verify environment status is updated
			Eventually(func() string {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Namespace
			}, timeout, interval).Should(Equal(expectedNsName))

			// Verify the RoleBinding was created in the namespace
			rbLookupKey := types.NamespacedName{Name: "quix-environment-access", Namespace: expectedNsName}
			createdRb := &rbacv1.RoleBinding{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, rbLookupKey, createdRb)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			// Verify status phase is Ready
			Eventually(func() quixiov1.EnvironmentPhase {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady))

			// Clean up
			By("Deleting the Environment")
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			// Add debugging for deletion
			GinkgoWriter.Printf("Environment deletion requested. Checking finalizer and deletion timestamp...\n")

			// Wait for the environment to be marked for deletion first
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					GinkgoWriter.Printf("Failed to get Environment during deletion: %v\n", err)
					return false
				}
				isBeingDeleted := !createdEnv.DeletionTimestamp.IsZero()
				GinkgoWriter.Printf("Environment being deleted: %v, Phase: %s, ResourceVersion: %s\n",
					isBeingDeleted,
					createdEnv.Status.Phase,
					createdEnv.ResourceVersion)
				return isBeingDeleted
			}, timeout, interval).Should(BeTrue())

			// Verify environment is eventually deleted
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if errors.IsNotFound(err) {
					GinkgoWriter.Printf("Environment was deleted successfully\n")
					return true
				}

				if err != nil {
					GinkgoWriter.Printf("Error checking environment deletion: %v\n", err)
					return false
				}

				GinkgoWriter.Printf("Environment still exists (Phase: %s, DeletionTimestamp: %v)\n",
					createdEnv.Status.Phase,
					createdEnv.DeletionTimestamp)
				return false
			}, timeout*2, interval).Should(BeTrue(), "Environment was not deleted in time")

			// Check namespace status - for test environments consider a namespace with deletion timestamp as deleted
			nsAfterDelete := &corev1.Namespace{}
			nsGetErr := k8sClient.Get(ctx, nsLookupKey, nsAfterDelete)
			if errors.IsNotFound(nsGetErr) {
				GinkgoWriter.Printf("Namespace was completely deleted\n")
			} else if nsGetErr != nil {
				GinkgoWriter.Printf("Error checking namespace: %v\n", nsGetErr)
			} else {
				GinkgoWriter.Printf("Namespace exists, checking deletion timestamp: %v\n", nsAfterDelete.DeletionTimestamp)
				// Consider a namespace with deletion timestamp as effectively deleted
				Expect(nsAfterDelete.DeletionTimestamp).NotTo(BeNil(), "Namespace should have a deletion timestamp")
				Expect(nsAfterDelete.DeletionTimestamp.IsZero()).To(BeFalse(), "Namespace should have a non-zero deletion timestamp")
			}
		})

		It("Should apply custom labels and annotations", func() {
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

			env := createTestEnvironment("test-custom", labels, annotations)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Expected namespace name
			expectedNsName := fmt.Sprintf("%s%s", env.Spec.Id, testConfig.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: expectedNsName}
			createdNs := &corev1.Namespace{}

			// Verify namespace was created with custom labels and annotations
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			// Verify custom labels were applied
			Expect(createdNs.Labels["custom-label"]).To(Equal("test-value"))
			Expect(createdNs.Labels["environment-type"]).To(Equal("test"))

			// Verify custom annotations were applied
			Expect(createdNs.Annotations["custom-annotation"]).To(Equal("test-value"))
			Expect(createdNs.Annotations["description"]).To(Equal("Test environment"))

			// Clean up
			By("Deleting the Environment")
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())
		})
	})

	// New test to verify direct namespace creation and deletion
	Context("When directly creating and deleting a namespace", func() {
		It("Should successfully create and delete the namespace", func() {
			ctx := context.Background()

			// Create a unique namespace name for this test
			testNsName := fmt.Sprintf("test-direct-ns-%d", time.Now().Unix())

			By(fmt.Sprintf("Directly creating namespace %s", testNsName))
			GinkgoWriter.Printf("Creating namespace %s directly with Kubernetes API\n", testNsName)

			// Create the namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: testNsName,
					Labels: map[string]string{
						"test.quix.io/direct-test": "true",
					},
				},
			}

			err := k8sClient.Create(ctx, ns)
			Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace")
			GinkgoWriter.Printf("Created namespace %s successfully\n", testNsName)

			// Get the namespace to verify it exists
			createdNs := &corev1.Namespace{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: testNsName}, createdNs)
			Expect(err).NotTo(HaveOccurred(), "Failed to get created namespace")
			GinkgoWriter.Printf("Retrieved namespace %s: UID=%s, ResourceVersion=%s, Labels=%v, Finalizers=%v\n",
				createdNs.Name,
				createdNs.UID,
				createdNs.ResourceVersion,
				createdNs.Labels,
				createdNs.Finalizers)

			// List all namespaces to verify
			var nsList corev1.NamespaceList
			err = k8sClient.List(ctx, &nsList)
			Expect(err).NotTo(HaveOccurred(), "Failed to list namespaces")
			GinkgoWriter.Printf("Found %d namespaces in test cluster:\n", len(nsList.Items))
			for _, ns := range nsList.Items {
				GinkgoWriter.Printf("  - %s (UID: %s, Labels: %v, Finalizers: %v)\n",
					ns.Name, ns.UID, ns.Labels, ns.Finalizers)
			}

			By(fmt.Sprintf("Directly deleting namespace %s", testNsName))
			GinkgoWriter.Printf("Deleting namespace %s directly with Kubernetes API\n", testNsName)

			// First check if namespace has any finalizers
			if len(createdNs.Finalizers) > 0 {
				GinkgoWriter.Printf("Namespace has finalizers: %v, attempting to remove\n", createdNs.Finalizers)

				// Patch to remove finalizers
				patchFinalizers := []byte(`{"metadata":{"finalizers":[]}}`)
				err = k8sClient.Patch(ctx, createdNs, client.RawPatch(types.MergePatchType, patchFinalizers))
				if err != nil {
					GinkgoWriter.Printf("WARNING: Failed to remove finalizers: %v\n", err)
				} else {
					GinkgoWriter.Printf("Successfully removed finalizers\n")
				}
			}

			// Delete with force option
			deleteOptions := &client.DeleteOptions{
				GracePeriodSeconds: &[]int64{0}[0],
			}

			err = k8sClient.Delete(ctx, createdNs, deleteOptions)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete test namespace")
			GinkgoWriter.Printf("Delete request for namespace %s sent successfully\n", testNsName)

			// Verify namespace is deleted with a longer timeout - but consider DeletionTimestamp as "effectively deleted"
			const longTimeout = time.Second * 20
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: testNsName}, createdNs)
				if errors.IsNotFound(err) {
					GinkgoWriter.Printf("Namespace %s confirmed deleted\n", testNsName)
					return true
				}
				if err != nil {
					GinkgoWriter.Printf("Error checking namespace: %v\n", err)
					return false
				}

				// For test environments: consider a namespace with deletion timestamp as effectively deleted
				if createdNs.DeletionTimestamp != nil && !createdNs.DeletionTimestamp.IsZero() {
					GinkgoWriter.Printf("Namespace %s has deletion timestamp %v - considering it deleted for test purposes\n",
						testNsName, createdNs.DeletionTimestamp)
					return true
				}

				GinkgoWriter.Printf("Namespace %s still exists without deletion timestamp\n", testNsName)

				// If namespace is stuck in terminating state for too long, try removing finalizers again
				if len(createdNs.Finalizers) > 0 {
					GinkgoWriter.Printf("Namespace has finalizers preventing deletion: %v\n", createdNs.Finalizers)
					patchFinalizers := []byte(`{"metadata":{"finalizers":[]}}`)
					if patchErr := k8sClient.Patch(ctx, createdNs, client.RawPatch(types.MergePatchType, patchFinalizers)); patchErr != nil {
						GinkgoWriter.Printf("Failed to remove finalizers: %v\n", patchErr)
					}
				}

				return false
			}, longTimeout, interval).Should(BeTrue(), "Namespace was not marked for deletion within timeout period")

			// Final check just to see if the namespace still exists in any form
			finalNs := &corev1.Namespace{}
			finalErr := k8sClient.Get(ctx, types.NamespacedName{Name: testNsName}, finalNs)

			// Log the status regardless of outcome
			if errors.IsNotFound(finalErr) {
				GinkgoWriter.Printf("Final check: Namespace %s completely gone from the API\n", testNsName)
			} else if finalErr != nil {
				GinkgoWriter.Printf("Final check: Error checking namespace: %v\n", finalErr)
			} else {
				GinkgoWriter.Printf("Final check: Namespace still exists with DeletionTimestamp=%v, Finalizers=%v\n",
					finalNs.DeletionTimestamp, finalNs.Finalizers)
				// This is expected - we'll pass the test anyway because in envtest namespaces likely won't be fully removed
			}
		})
	})

	Context("When creating an Environment resource with invalid ID", func() {
		It("Should set phase to CreateFailed and set error condition", func() {
			By("Creating an Environment with ID that generates a too-long namespace name")
			ctx := context.Background()

			// Save the original suffix and restore after test
			originalSuffix := testConfig.NamespaceSuffix
			defer func() { testConfig.NamespaceSuffix = originalSuffix }()

			// Set a long suffix that, when combined with a valid ID, exceeds 63 chars
			testConfig.NamespaceSuffix = "-with-a-very-long-suffix-that-makes-the-namespace-name-too-long-for-kubernetes"

			// Use a valid ID format within the 44 char limit
			validId := "valid-id"

			// Calculate the total namespace name that will be generated
			namespaceNameThatWouldBeGenerated := validId + testConfig.NamespaceSuffix
			GinkgoWriter.Printf("Test will generate namespace name: %s (length: %d)\n",
				namespaceNameThatWouldBeGenerated, len(namespaceNameThatWouldBeGenerated))

			invalidEnv := &quixiov1.Environment{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "quix.io/v1",
					Kind:       "Environment",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "namespace-too-long-test",
					Namespace: "default",
				},
				Spec: quixiov1.EnvironmentSpec{
					Id: validId, // Valid format but will generate a namespace name > 63 chars with our test suffix
				},
			}

			Expect(k8sClient.Create(ctx, invalidEnv)).Should(Succeed())

			// Look up the environment resource after creation
			envLookupKey := types.NamespacedName{Name: invalidEnv.Name, Namespace: testNamespace}
			createdEnv := &quixiov1.Environment{}

			// Verify the environment was created and has failed phase
			Eventually(func() quixiov1.EnvironmentPhase {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return ""
				}
				GinkgoWriter.Printf("Environment phase: %s, Error: %s\n",
					createdEnv.Status.Phase, createdEnv.Status.ErrorMessage)
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseCreateFailed))

			// Verify error message is set and mentions namespace length
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return false
				}
				return len(createdEnv.Status.ErrorMessage) > 0 &&
					strings.Contains(createdEnv.Status.ErrorMessage, "exceeds")
			}, timeout, interval).Should(BeTrue())

			// Verify Ready condition is set to False with ValidationError reason
			Eventually(func() string {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return ""
				}

				for _, condition := range createdEnv.Status.Conditions {
					if condition.Type == "Ready" {
						return string(condition.Status)
					}
				}
				return ""
			}, timeout, interval).Should(Equal(string(metav1.ConditionFalse)))

			// Clean up
			Expect(k8sClient.Delete(ctx, invalidEnv)).Should(Succeed())
		})
	})

	Context("When updating an Environment resource", func() {
		It("Should propagate label and annotation changes to the namespace", func() {
			By("Creating a new Environment")
			ctx := context.Background()
			env := createTestEnvironment("test-update", map[string]string{"initial": "value"}, map[string]string{"init-anno": "value"})
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Expected namespace name
			expectedNsName := fmt.Sprintf("%s%s", env.Spec.Id, testConfig.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: expectedNsName}

			// Wait for namespace to be created
			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			// Verify initial labels
			Expect(createdNs.Labels["initial"]).To(Equal("value"))

			// Wait for environment to reach Ready state
			envLookupKey := types.NamespacedName{Name: env.Name, Namespace: testNamespace}
			createdEnv := &quixiov1.Environment{}

			Eventually(func() quixiov1.EnvironmentPhase {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady))

			// Update the environment with new labels and annotations
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

			// Optionally check for Updating phase (might be too quick to catch in tests)
			By("Checking if the environment enters Updating phase")
			updatingFound := false
			for attempt := 0; attempt < 10; attempt++ {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err == nil {
					if createdEnv.Status.Phase == quixiov1.PhaseUpdating {
						updatingFound = true
						break
					}
				}
				time.Sleep(50 * time.Millisecond)
			}

			// Don't fail test if we didn't catch the Updating phase; it might transition too quickly
			if updatingFound {
				GinkgoWriter.Printf("Caught environment in Updating phase\n")
			}

			// Verify namespace was updated with the new labels and annotations
			Eventually(func() string {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				if err != nil {
					return ""
				}
				return createdNs.Labels["new-label"]
			}, timeout, interval).Should(Equal("new-value"))

			Eventually(func() string {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				if err != nil {
					return ""
				}
				return createdNs.Annotations["new-anno"]
			}, timeout, interval).Should(Equal("new-value"))

			// Verify environment returned to Ready state
			Eventually(func() quixiov1.EnvironmentPhase {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return ""
				}
				GinkgoWriter.Printf("Current environment phase: %s\n", createdEnv.Status.Phase)
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady))

			// Clean up
			By("Deleting the Environment")
			Expect(k8sClient.Delete(ctx, updatedEnv)).Should(Succeed())
		})
	})

	Context("When checking environment conditions", func() {
		It("Should set all required conditions during lifecycle", func() {
			By("Creating a new Environment")
			ctx := context.Background()
			env := createTestEnvironment("test-conditions", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			// Look up the environment resource after creation
			envLookupKey := types.NamespacedName{Name: env.Name, Namespace: testNamespace}
			createdEnv := &quixiov1.Environment{}

			// Verify the NamespaceCreated condition is set to True
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return false
				}

				for _, condition := range createdEnv.Status.Conditions {
					if condition.Type == "NamespaceCreated" && condition.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Verify the RoleBindingCreated condition is set to True
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return false
				}

				for _, condition := range createdEnv.Status.Conditions {
					if condition.Type == "RoleBindingCreated" && condition.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Verify the Ready condition is eventually set to True
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return false
				}

				for _, condition := range createdEnv.Status.Conditions {
					if condition.Type == "Ready" && condition.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Clean up
			By("Deleting the Environment")
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			// Verify conditions are updated during deletion
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if errors.IsNotFound(err) {
					return true // Environment was fully deleted
				}

				if err != nil {
					return false
				}

				// In deletion phase, Ready condition should be false
				for _, condition := range createdEnv.Status.Conditions {
					if condition.Type == "Ready" && condition.Status == metav1.ConditionFalse {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})
	})
})
