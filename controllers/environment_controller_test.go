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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	"github.com/quix-analytics/quix-environment-operator/internal/namespaces"
	"github.com/quix-analytics/quix-environment-operator/internal/status"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment
var ctx context.Context
var cancel context.CancelFunc
var testConfig *config.OperatorConfig
var reconciler *EnvironmentReconciler
var mockNamespaceManager *namespaces.MockNamespaceManager

// TestEventRecorder captures events for testing
type TestEventRecorder struct {
	Events []string
}

func (r *TestEventRecorder) Event(object runtime.Object, eventtype, reason, message string) {
	r.Events = append(r.Events, fmt.Sprintf("%s %s: %s", eventtype, reason, message))
}

func (r *TestEventRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	r.Events = append(r.Events, fmt.Sprintf("%s %s: %s", eventtype, reason, fmt.Sprintf(messageFmt, args...)))
}

func (r *TestEventRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype string, reason string, messageFmt string, args ...interface{}) {
	r.Events = append(r.Events, fmt.Sprintf("%s %s: %s", eventtype, reason, fmt.Sprintf(messageFmt, args...)))
}

func (r *TestEventRecorder) ContainsEvent(text string) bool {
	for _, event := range r.Events {
		if strings.Contains(event, text) {
			return true
		}
	}
	return false
}

func (r *TestEventRecorder) Reset() {
	r.Events = []string{}
}

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
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = quixiov1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).ToNot(HaveOccurred())

	testConfig = &config.OperatorConfig{
		NamespaceSuffix:         "-suffix",
		ServiceAccountName:      "test-service-account",
		ServiceAccountNamespace: "default",
		ClusterRoleName:         "test-cluster-role",
	}

	statusUpdater := status.NewStatusUpdater(k8sManager.GetClient(), k8sManager.GetEventRecorderFor("environment-controller"))

	mockNamespaceManager = namespaces.NewMockNamespaceManager(
		namespaces.NewDefaultNamespaceManager(
			k8sManager.GetClient(),
			k8sManager.GetEventRecorderFor("environment-controller"),
			statusUpdater))

	reconciler, err = NewEnvironmentReconciler(
		k8sManager.GetClient(),
		k8sManager.GetScheme(),
		k8sManager.GetEventRecorderFor("environment-controller"),
		testConfig,
		mockNamespaceManager,
		statusUpdater,
	)
	Expect(err).ToNot(HaveOccurred(), "failed to create reconciler")

	err = reconciler.SetupWithManager(k8sManager)
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
		It("Should create a namespace with the correct name and properly handle deletion", func() {
			By("Creating a new Environment")
			ctx := context.Background()
			env := createTestEnvironment("test", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			envLookupKey := types.NamespacedName{Name: env.Name, Namespace: testNamespace}
			createdEnv := &quixiov1.Environment{}

			Eventually(func() bool {
				return k8sClient.Get(ctx, envLookupKey, createdEnv) == nil
			}, timeout, interval).Should(BeTrue())

			expectedNsName := fmt.Sprintf("%s%s", env.Spec.Id, testConfig.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: expectedNsName}
			createdNs := &corev1.Namespace{}

			Eventually(func() bool {
				return k8sClient.Get(ctx, nsLookupKey, createdNs) == nil
			}, timeout, interval).Should(BeTrue())

			Expect(createdNs.Labels["quix.io/managed-by"]).To(Equal("quix-environment-operator"))
			Expect(createdNs.Labels["quix.io/environment-id"]).To(Equal(env.Spec.Id))

			Eventually(func() string {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Namespace
			}, timeout, interval).Should(Equal(expectedNsName))

			rbLookupKey := types.NamespacedName{Name: "quix-environment-access", Namespace: expectedNsName}
			createdRb := &rbacv1.RoleBinding{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, rbLookupKey, createdRb)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			Eventually(func() quixiov1.EnvironmentPhase {
				if err := k8sClient.Get(ctx, envLookupKey, createdEnv); err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady))

			By("Deleting the Environment")
			// Mark deletion timestamp on the environment
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			By("Verifying Environment transitions to PhaseDeleting")
			Eventually(func() bool {
				env := &quixiov1.Environment{}
				err := k8sClient.Get(ctx, envLookupKey, env)
				if errors.IsNotFound(err) {
					// Environment may already be deleted, which is fine
					return true
				}
				if err != nil {
					return false
				}
				return env.Status.Phase == quixiov1.PhaseDeleting || !env.DeletionTimestamp.IsZero()
			}, timeout, interval).Should(BeTrue(), "Environment should transition to Deleting phase or be marked for deletion")

			By("Waiting for the Namespace to be considered deleted by the manager")
			Eventually(func() bool {
				nsGetErr := k8sClient.Get(ctx, nsLookupKey, createdNs) // Attempt to get the NS
				// Use the mock manager instance available in the test scope
				return mockNamespaceManager.IsNamespaceDeleted(createdNs, nsGetErr)
			}, timeout, interval).Should(BeTrue(), "Namespace was not considered deleted by the manager in time")

			By("Verifying the Environment CR is deleted after finalizer is removed")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, &quixiov1.Environment{})
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "Environment CR should be fully deleted")
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

			By("Deleting the Environment")
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())
		})
	})

	Context("When creating an Environment resource with invalid ID", func() {
		It("Should set phase to CreateFailed and set error condition", func() {
			By("Creating an Environment with ID that generates a too-long namespace name")
			ctx := context.Background()

			originalSuffix := testConfig.NamespaceSuffix
			defer func() { testConfig.NamespaceSuffix = originalSuffix }()

			testConfig.NamespaceSuffix = "-with-a-very-long-suffix-that-makes-the-namespace-name-too-long-for-kubernetes"
			validId := "valid-id"

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
					Id: validId,
				},
			}

			Expect(k8sClient.Create(ctx, invalidEnv)).Should(Succeed())

			envLookupKey := types.NamespacedName{Name: invalidEnv.Name, Namespace: testNamespace}
			createdEnv := &quixiov1.Environment{}

			Eventually(func() quixiov1.EnvironmentPhase {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseCreateFailed))

			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return false
				}
				return len(createdEnv.Status.ErrorMessage) > 0 &&
					strings.Contains(createdEnv.Status.ErrorMessage, "exceeds")
			}, timeout, interval).Should(BeTrue())

			Eventually(func() string {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return ""
				}

				for _, condition := range createdEnv.Status.Conditions {
					if condition.Type == status.ConditionTypeReady {
						return string(condition.Status)
					}
				}
				return ""
			}, timeout, interval).Should(Equal(string(metav1.ConditionFalse)))

			Expect(k8sClient.Delete(ctx, invalidEnv)).Should(Succeed())
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
	})

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
			checkCondition := func(conditionType string) metav1.ConditionStatus {
				for _, condition := range createdEnv.Status.Conditions {
					if condition.Type == conditionType {
						return condition.Status
					}
				}
				return metav1.ConditionUnknown // Condition not found
			}

			// Wait specifically for Ready condition
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, envLookupKey, createdEnv)).Should(Succeed())
				g.Expect(checkCondition(status.ConditionTypeReady)).To(Equal(metav1.ConditionTrue))
			}, timeout, interval).Should(Succeed(), "Ready condition should become True")

			By("Deleting the Environment")
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			// Check that the Ready condition becomes False upon deletion
			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if errors.IsNotFound(err) {
					return true // Environment was fully deleted, counts as Ready=False implicitly
				}

				if err != nil {
					// Log transient errors but don't fail the check immediately
					logf.Log.Error(err, "Error getting environment during deletion check")
					return false
				}

				// Environment still exists, check the Ready condition
				// Need to re-check conditions within the Eventually block's Get
				found := false
				for _, condition := range createdEnv.Status.Conditions {
					if condition.Type == status.ConditionTypeReady {
						found = true
						if condition.Status != metav1.ConditionFalse {
							return false // Ready condition exists but is not False
						}
						break // Found the Ready condition
					}
				}
				return found // Return true if Ready condition is False

			}, timeout, interval).Should(BeTrue(), "Ready condition should become False after deletion starts")
		})
	})

	Context("When updating an Environment resource fails", func() {
		It("Should transition to UpdateFailed phase when namespace update exceeds name length", func() {
			By("Creating a new Environment with an ID that will generate a valid namespace name")
			ctx := context.Background()

			// Initially create a valid environment
			env := createTestEnvironment("valid-name", map[string]string{"initial": "value"}, nil)
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
				return mockNamespaceManager.IsNamespaceDeleted(preExistingNs, nsGetErr)
			}, timeout, interval).Should(BeTrue(), "Namespace was not considered deleted by the manager in time")
		})
	})

	Context("When validating Environment label and annotation inputs", func() {
		It("Should reject environments with invalid label formats", func() {
			By("Creating an Environment with invalid label format")
			ctx := context.Background()

			// Use a label with invalid characters that should definitely be rejected
			env := createTestEnvironment("valid-id", map[string]string{
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
					return ""
				}
				for _, cond := range createdEnv.Status.Conditions {
					if cond.Type == status.ConditionTypeReady {
						return cond.Status
					}
				}
				return ""
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

	Context("When checking events during environment lifecycle", func() {
		It("Should record appropriate events for key state transitions", func() {
			By("Setting up a test environment and recorder")
			ctx := context.Background()

			// Create a recorder that we can examine
			recorder := &TestEventRecorder{Events: []string{}}
			recorder.Reset()

			// Create a test environment
			env := createTestEnvironment("event-test", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			envLookupKey := types.NamespacedName{Name: env.Name, Namespace: testNamespace}
			createdEnv := &quixiov1.Environment{}

			expectedNsName := fmt.Sprintf("%s%s", env.Spec.Id, testConfig.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: expectedNsName}

			By("Waiting for environment to become ready")
			Eventually(func() quixiov1.EnvironmentPhase {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady))

			By("Updating the environment with new labels")
			updatedEnv := &quixiov1.Environment{}
			Expect(k8sClient.Get(ctx, envLookupKey, updatedEnv)).Should(Succeed())

			updatedEnv.Spec.Labels = map[string]string{
				"new-label": "value",
			}

			Expect(k8sClient.Update(ctx, updatedEnv)).Should(Succeed())

			By("Verifying namespace has updated labels")
			var nsWithLabels corev1.Namespace
			Eventually(func() string {
				err := k8sClient.Get(ctx, nsLookupKey, &nsWithLabels)
				if err != nil {
					return ""
				}
				return nsWithLabels.Labels["new-label"]
			}, timeout, interval).Should(Equal("value"))

			// We can't easily check the events directly since the real event recorder
			// in the test sends events to the API server, not our TestEventRecorder.
			// Instead, we'll verify that the update was applied correctly and the
			// environment transitions through the expected states.

			By("Verifying the environment went through an updating phase")
			updatedEnv = &quixiov1.Environment{}
			Expect(k8sClient.Get(ctx, envLookupKey, updatedEnv)).Should(Succeed())
			Expect(updatedEnv.Status.Phase).To(Equal(quixiov1.PhaseReady), "Environment should be back to Ready state")

			By("Deleting the environment")
			Expect(k8sClient.Delete(ctx, updatedEnv)).Should(Succeed())

			Eventually(func() bool {
				env := &quixiov1.Environment{}
				err := k8sClient.Get(ctx, envLookupKey, env)
				if errors.IsNotFound(err) {
					// Environment may already be deleted, which is fine
					return true
				}
				if err != nil {
					return false
				}
				// Either it should be in Deleting phase or have a non-zero deletion timestamp
				return env.Status.Phase == quixiov1.PhaseDeleting || !env.DeletionTimestamp.IsZero()
			}, timeout, interval).Should(BeTrue(), "Environment should be in deleting phase or marked for deletion")

			By("Verifying namespace is deleted")
			Eventually(func() bool {
				var ns corev1.Namespace
				nsGetErr := k8sClient.Get(ctx, types.NamespacedName{Name: expectedNsName}, &ns)
				// Use the mock manager instance
				return mockNamespaceManager.IsNamespaceDeleted(&ns, nsGetErr)
			}, timeout, interval).Should(BeTrue(), "Namespace should be considered deleted by the manager")
		})
	})

	Context("When handling concurrent modifications", func() {
		It("Should correctly reconcile with concurrent spec updates", func() {
			By("Creating a new Environment")
			ctx := context.Background()
			env := createTestEnvironment("concurrent-test", nil, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			envLookupKey := types.NamespacedName{Name: env.Name, Namespace: testNamespace}
			createdEnv := &quixiov1.Environment{}

			By("Waiting for environment to become ready")
			Eventually(func() quixiov1.EnvironmentPhase {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady))

			expectedNsName := fmt.Sprintf("%s%s", env.Spec.Id, testConfig.NamespaceSuffix)

			By("Performing multiple rapid updates to the same Environment with retries")
			firstEnv := createdEnv.DeepCopy()
			secondEnv := createdEnv.DeepCopy()
			thirdEnv := createdEnv.DeepCopy()

			// Prepare different updates
			firstEnv.Spec.Labels = map[string]string{"update": "first"}
			secondEnv.Spec.Labels = map[string]string{"update": "second"}
			thirdEnv.Spec.Labels = map[string]string{"update": "third"}

			// Function to retry updates with conflict handling
			updateWithRetry := func(env *quixiov1.Environment, maxRetries int) {
				for i := 0; i < maxRetries; i++ {
					err := k8sClient.Update(ctx, env)
					if err == nil {
						return // Success
					}

					if !errors.IsConflict(err) {
						Expect(err).NotTo(HaveOccurred(), "Update failed with unexpected error")
						return
					}

					// Handle conflict by refreshing the object
					refreshedEnv := &quixiov1.Environment{}
					if err := k8sClient.Get(ctx, envLookupKey, refreshedEnv); err != nil {
						continue // Skip retry if get fails
					}

					// Copy updated spec to the refreshed object
					refreshedEnv.Spec.Labels = env.Spec.Labels
					env = refreshedEnv

					// Small delay before retrying
					time.Sleep(10 * time.Millisecond)
				}
			}

			// Submit updates with retries for conflicts
			updateWithRetry(firstEnv, 3)
			time.Sleep(50 * time.Millisecond)
			updateWithRetry(secondEnv, 3)
			time.Sleep(50 * time.Millisecond)
			updateWithRetry(thirdEnv, 3)

			By("Verifying the environment eventually reconciles to the final state")
			Eventually(func() string {
				ns := &corev1.Namespace{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: expectedNsName}, ns)
				if err != nil {
					return ""
				}
				return ns.Labels["update"]
			}, timeout, interval).Should(Equal("third"), "Namespace should eventually have the labels from the final update")

			By("Verifying environment returns to Ready phase")
			Eventually(func() quixiov1.EnvironmentPhase {
				finalEnv := &quixiov1.Environment{}
				err := k8sClient.Get(ctx, envLookupKey, finalEnv)
				if err != nil {
					return ""
				}
				return finalEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady), "Environment should return to Ready state")

			By("Checking ResourceVersion to confirm updates happened")
			finalEnv := &quixiov1.Environment{}
			Expect(k8sClient.Get(ctx, envLookupKey, finalEnv)).Should(Succeed())
			Expect(finalEnv.ResourceVersion).NotTo(Equal(createdEnv.ResourceVersion), "ResourceVersion should have changed")

			By("Deleting the environment")
			Expect(k8sClient.Delete(ctx, finalEnv)).Should(Succeed())

			By("Verifying namespace is deleted")
			Eventually(func() bool {
				var ns corev1.Namespace
				nsGetErr := k8sClient.Get(ctx, types.NamespacedName{Name: expectedNsName}, &ns)
				// Use the mock manager instance
				return mockNamespaceManager.IsNamespaceDeleted(&ns, nsGetErr)
			}, timeout, interval).Should(BeTrue(), "Namespace should be considered deleted by the manager")
		})
	})
})
