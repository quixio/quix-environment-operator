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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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

// TestingNamespaceManager extends NamespaceManager with test-specific functionality
type TestingNamespaceManager interface {
	namespaces.NamespaceManager
}

// testNamespaceManager implements the TestingNamespaceManager interface
type testNamespaceManager struct {
	namespaces.NamespaceManager
}

// NewTestingNamespaceManager creates a new testing namespace manager that wraps a regular manager
func NewTestingNamespaceManager(baseManager namespaces.NamespaceManager) TestingNamespaceManager {
	return &testNamespaceManager{
		NamespaceManager: baseManager,
	}
}

// IsNamespaceDeleted checks if a namespace should be considered deleted
func (m *testNamespaceManager) IsNamespaceDeleted(namespace *corev1.Namespace, err error) bool {
	if err != nil {
		return errors.IsNotFound(err)
	}
	return namespace == nil || (namespace.DeletionTimestamp != nil && !namespace.DeletionTimestamp.IsZero())
}

func (m *testNamespaceManager) LogType() string {
	return "TestingNamespaceManager"
}

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

	baseNamespaceManager := namespaces.NewDefaultNamespaceManager(
		k8sManager.GetClient(),
		k8sManager.GetEventRecorderFor("environment-controller"),
		statusUpdater,
	)

	testingNamespaceManager := NewTestingNamespaceManager(baseNamespaceManager)

	reconciler, err = NewEnvironmentReconciler(
		k8sManager.GetClient(),
		k8sManager.GetScheme(),
		k8sManager.GetEventRecorderFor("environment-controller"),
		testConfig,
		testingNamespaceManager,
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
	const timeout = time.Second * 4
	const interval = time.Millisecond * 250
	const testNamespace = "default"

	Context("When creating an Environment resource", func() {
		It("Should create a namespace with the correct name", func() {
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
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "Environment was not deleted in time")

			nsAfterDelete := &corev1.Namespace{}
			nsGetErr := k8sClient.Get(ctx, nsLookupKey, nsAfterDelete)
			if !errors.IsNotFound(nsGetErr) && nsGetErr == nil {
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

	Context("When directly creating and deleting a namespace", func() {
		It("Should successfully create and delete the namespace", func() {
			ctx := context.Background()
			testNsName := fmt.Sprintf("test-direct-ns-%d", time.Now().Unix())

			By(fmt.Sprintf("Directly creating namespace %s", testNsName))
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

			createdNs := &corev1.Namespace{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: testNsName}, createdNs)
			Expect(err).NotTo(HaveOccurred(), "Failed to get created namespace")

			By(fmt.Sprintf("Directly deleting namespace %s", testNsName))
			if len(createdNs.Finalizers) > 0 {
				patchFinalizers := []byte(`{"metadata":{"finalizers":[]}}`)
				err = k8sClient.Patch(ctx, createdNs, client.RawPatch(types.MergePatchType, patchFinalizers))
			}

			deleteOptions := &client.DeleteOptions{
				GracePeriodSeconds: &[]int64{0}[0],
			}

			err = k8sClient.Delete(ctx, createdNs, deleteOptions)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete test namespace")

			const longTimeout = time.Second * 8
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: testNsName}, createdNs)
				if errors.IsNotFound(err) {
					return true
				}
				if err != nil {
					return false
				}

				if createdNs.DeletionTimestamp != nil && !createdNs.DeletionTimestamp.IsZero() {
					return true
				}

				if len(createdNs.Finalizers) > 0 {
					patchFinalizers := []byte(`{"metadata":{"finalizers":[]}}`)
					_ = k8sClient.Patch(ctx, createdNs, client.RawPatch(types.MergePatchType, patchFinalizers))
				}

				return false
			}, longTimeout, interval).Should(BeTrue(), "Namespace was not marked for deletion within timeout period")
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
					if condition.Type == "Ready" {
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

			By("Deleting the Environment")
			Expect(k8sClient.Delete(ctx, env)).Should(Succeed())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if errors.IsNotFound(err) {
					return true // Environment was fully deleted
				}

				if err != nil {
					return false
				}

				for _, condition := range createdEnv.Status.Conditions {
					if condition.Type == "Ready" && condition.Status == metav1.ConditionFalse {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When updating an Environment resource fails", func() {
		It("Should transition to UpdateFailed phase when namespace update fails", func() {
			By("Creating a new Environment")
			ctx := context.Background()
			env := createTestEnvironment("test-update-fail", map[string]string{"initial": "value"}, nil)
			Expect(k8sClient.Create(ctx, env)).Should(Succeed())

			expectedNsName := fmt.Sprintf("%s%s", env.Spec.Id, testConfig.NamespaceSuffix)
			nsLookupKey := types.NamespacedName{Name: expectedNsName}

			createdNs := &corev1.Namespace{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nsLookupKey, createdNs)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			envLookupKey := types.NamespacedName{Name: env.Name, Namespace: testNamespace}
			createdEnv := &quixiov1.Environment{}

			Eventually(func() quixiov1.EnvironmentPhase {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseReady))

			By("Creating a test event recorder to capture events")
			testRecorder := &TestEventRecorder{}

			mockManager := &namespaces.MockNamespaceManager{
				UpdateMetadataFunc: func(ctx context.Context, env *quixiov1.Environment, namespace *corev1.Namespace) error {
					return fmt.Errorf("simulated update failure for testing")
				},
				ApplyMetadataFunc: func(env *quixiov1.Environment, namespace *corev1.Namespace) bool {
					if namespace.Labels == nil {
						namespace.Labels = make(map[string]string)
					}
					namespace.Labels["quix.io/managed-by"] = "quix-environment-operator"
					namespace.Labels["quix.io/environment-id"] = env.Spec.Id
					return true
				},
				IsNamespaceDeletedFunc: func(namespace *corev1.Namespace, err error) bool {
					if err != nil {
						return errors.IsNotFound(err)
					}
					return namespace == nil || (namespace.DeletionTimestamp != nil && !namespace.DeletionTimestamp.IsZero())
				},
			}

			statusUpdater := status.NewStatusUpdater(k8sClient, testRecorder)
			errorReconciler, err := NewEnvironmentReconciler(
				k8sClient,
				scheme.Scheme,
				testRecorder,
				testConfig,
				mockManager,
				statusUpdater,
			)
			Expect(err).NotTo(HaveOccurred())

			updatedEnv := createdEnv.DeepCopy()
			updatedEnv.Spec.Annotations = map[string]string{
				"will-fail": "update-will-fail-due-to-mock",
			}

			Expect(k8sClient.Update(ctx, updatedEnv)).Should(Succeed())

			Eventually(func() bool {
				_, err := errorReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: envLookupKey,
				})
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying that update failure events are emitted")
			Eventually(func() bool {
				return testRecorder.ContainsEvent("simulated update failure")
			}, timeout, interval).Should(BeTrue(), "Expected to see an event about the update failure")

			By("Verifying environment phase changes to UpdateFailed")
			Eventually(func() quixiov1.EnvironmentPhase {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return ""
				}
				return createdEnv.Status.Phase
			}, timeout, interval).Should(Equal(quixiov1.PhaseUpdateFailed))

			Eventually(func() bool {
				err := k8sClient.Get(ctx, envLookupKey, createdEnv)
				if err != nil {
					return false
				}
				return strings.Contains(createdEnv.Status.ErrorMessage, "simulated update failure")
			}, timeout, interval).Should(BeTrue())

			var readyCondition *metav1.Condition
			for i := range createdEnv.Status.Conditions {
				if createdEnv.Status.Conditions[i].Type == "Ready" {
					readyCondition = &createdEnv.Status.Conditions[i]
					break
				}
			}
			Expect(readyCondition).NotTo(BeNil())
			Expect(string(readyCondition.Status)).To(Equal(string(metav1.ConditionFalse)))

			Expect(k8sClient.Delete(ctx, createdEnv)).Should(Succeed())
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
					if cond.Type == "Ready" {
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
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: expectedNsName}, retrievedNs)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "Pre-existing namespace was not deleted during cleanup")
		})
	})
})
