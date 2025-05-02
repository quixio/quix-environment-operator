package main

import (
	"flag"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	quixv1 "github.com/quix-analytics/quix-environment-operator/api/v1"
	"github.com/quix-analytics/quix-environment-operator/internal/config"
	environmentcontroller "github.com/quix-analytics/quix-environment-operator/internal/controller/environment"
	"github.com/quix-analytics/quix-environment-operator/internal/resources/environment"
	"github.com/quix-analytics/quix-environment-operator/internal/resources/namespace"
	"github.com/quix-analytics/quix-environment-operator/internal/resources/rolebinding"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(quixv1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var configPath string
	var syncPeriodStr string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&configPath, "config", "", "The path to the config file.")
	flag.StringVar(&syncPeriodStr, "sync-period", "10m", "The sync period for the cache.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Load operator configuration
	configLoader := config.NewEnvConfigLoader()
	operatorConfig, err := configLoader.LoadConfig()
	if err != nil {
		setupLog.Error(err, "unable to load operator config")
		os.Exit(1)
	}

	// Parse sync period duration
	syncPeriod := time.Minute * 10 // Default 10 minutes
	if syncPeriodStr != "" {
		d, err := time.ParseDuration(syncPeriodStr)
		if err != nil {
			setupLog.Error(err, "unable to parse sync period", "value", syncPeriodStr)
			os.Exit(1)
		}
		syncPeriod = d
	}

	// Override sync period with operator config if available
	if operatorConfig.CacheSyncPeriod != 0 {
		syncPeriod = operatorConfig.CacheSyncPeriod
		setupLog.Info("using configured cache sync period", "duration", syncPeriod)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: server.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "quix-environment-operator.quix-analytics.com",
		// Ensure we correctly handle cluster-scoped resources
		Cache: cache.Options{
			SyncPeriod: &syncPeriod,
			ByObject: map[client.Object]cache.ByObject{
				&quixv1.Environment{}: {}, // Enables caching for the cluster-scoped Environment CR
			},
		},
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Create environment manager
	environmentManager := environment.NewManager(mgr.GetClient(), mgr.GetAPIReader())

	// Create namespace manager with proper dependencies
	namespaceManager := namespace.NewManager(
		mgr.GetClient(),
		mgr.GetEventRecorderFor("environment-controller"),
		operatorConfig,
	)

	// Create role binding manager
	roleBindingManager := rolebinding.NewManager(
		mgr.GetClient(),
		operatorConfig,
		ctrl.Log.WithName("rolebinding-manager"),
		namespaceManager,
	)

	// Create and set up the environment controller
	if err = environmentcontroller.NewEnvironmentReconciler(
		mgr.GetClient(),
		mgr.GetScheme(),
		environmentManager,
		operatorConfig,
		namespaceManager,
		mgr.GetEventRecorderFor("environment-controller"),
		roleBindingManager,
	).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Environment")
		os.Exit(1)
	}

	// Set up health checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
