//go:build integration
// +build integration

package environment

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	quixiov1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
	qctrl "github.com/quix-analytics/quix-environment-operator/internal/controller/environment"
	"github.com/quix-analytics/quix-environment-operator/internal/resources/environment"
	"github.com/quix-analytics/quix-environment-operator/internal/resources/namespace"
	"github.com/quix-analytics/quix-environment-operator/internal/resources/rolebinding"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment
var ctx context.Context
var cancel context.CancelFunc
var k8sManager ctrl.Manager
var Controller *qctrl.EnvironmentReconciler
var Config *config.OperatorConfig // Exported configuration that tests can modify

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	// Increase test timeouts to ensure slow operations complete
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.Timeout = time.Minute * 2 // Allow up to 2 minutes for the entire suite
	suiteConfig.FailFast = false          // Don't stop on first failure

	RunSpecs(t, "Controller Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	// Set up logging for better debugging
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	logger := logf.Log.WithName("test-setup")

	ctx, cancel = context.WithCancel(context.TODO())

	logger.Info("Bootstrapping test environment")
	// Get the project root directory
	crdPaths := filepath.Join("..", "config", "crd", "bases")

	// Check CRD path exists
	if _, err := os.Stat(crdPaths); os.IsNotExist(err) {
		logger.Error(err, "CRD path does not exist", "path", crdPaths)
		// Try to create it
		if err := os.MkdirAll(crdPaths, 0755); err != nil {
			logger.Error(err, "Failed to create CRD directory")
		}
	}

	// Check if CRD files exist
	files, err := os.ReadDir(crdPaths)
	if err != nil {
		logger.Error(err, "Failed to read CRD directory")
	} else {
		logger.Info(fmt.Sprintf("Found %d files in CRD directory", len(files)))
		for _, file := range files {
			logger.Info("CRD file", "name", file.Name())
		}
	}

	_ = os.Setenv("KUBEBUILDER_ASSETS", os.Getenv("KUBEBUILDER_ASSETS"))
	logger.Info("KUBEBUILDER_ASSETS", "value", os.Getenv("KUBEBUILDER_ASSETS"))

	logger.Info("Starting test environment with CRDs", "path", crdPaths)
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{crdPaths},
		ErrorIfCRDPathMissing: true,
	}

	logger.Info("Starting test API server")
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	logger.Info("Adding the quix.io/v1 Environment CRD to the scheme")
	err = quixiov1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	logger.Info("Creating kubernetes client")
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// Set up the test configuration
	Config = &config.OperatorConfig{
		NamespaceSuffix:         "-suffix",
		ServiceAccountName:      "test-service-account",
		ServiceAccountNamespace: "default",
		ClusterRoleName:         "test-cluster-role",
		ReconcileInterval:       time.Millisecond * 250, // Faster reconciliation for tests
		MaxConcurrentReconciles: 5,
	}

	// Create default namespace if it doesn't exist
	defaultNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: Config.ServiceAccountNamespace,
		},
	}
	err = k8sClient.Create(ctx, defaultNs)
	if err != nil && client.IgnoreAlreadyExists(err) == nil {
		logger.Error(err, "Failed to create default namespace")
	}

	// Create service account that role binding will reference
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      Config.ServiceAccountName,
			Namespace: Config.ServiceAccountNamespace,
		},
	}
	err = k8sClient.Create(ctx, serviceAccount)
	if err != nil && client.IgnoreAlreadyExists(err) == nil {
		logger.Error(err, "Failed to create test service account")
	}
	logger.Info("Created test service account",
		"name", Config.ServiceAccountName,
		"namespace", Config.ServiceAccountNamespace)

	// Create the cluster role that role binding will reference
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: Config.ClusterRoleName,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "services", "configmaps", "secrets"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
		},
	}
	err = k8sClient.Create(ctx, clusterRole)
	if err != nil && client.IgnoreAlreadyExists(err) == nil {
		logger.Error(err, "Failed to create test cluster role")
	}
	logger.Info("Created test cluster role", "name", Config.ClusterRoleName)

	logger.Info("Setting up controller manager")
	k8sManager, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).ToNot(HaveOccurred())

	// Create required manager for the controller
	client := k8sManager.GetClient()
	envManager := environment.NewManager(client)

	// Create a namespace manager
	nsManager := namespace.NewManager(
		client,
		k8sManager.GetEventRecorderFor("test-controller"),
		Config,
	)

	// Create a rolebinding manager
	rbManager := rolebinding.NewManager(
		client,
		Config,
		logf.Log,
		nsManager,
	)

	// Create the controller with all dependencies
	logger.Info("Creating controller")
	Controller = qctrl.NewEnvironmentReconciler(
		client,
		k8sManager.GetScheme(),
		envManager,
		Config,
		nsManager,
		k8sManager.GetEventRecorderFor("test-controller"),
		rbManager,
	)

	logger.Info("Setting up controller with manager")
	err = Controller.SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	logger.Info("Starting manager")
	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(ctx)
		Expect(err).ToNot(HaveOccurred(), "failed to run manager")
	}()

	logger.Info("Test environment setup complete")
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
