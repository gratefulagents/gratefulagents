/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"strings"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	agentsandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	agentsandboxextensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/internal/buildinfo"
	platformcontroller "github.com/gratefulagents/gratefulagents/internal/controller/platform"
	triggercontroller "github.com/gratefulagents/gratefulagents/internal/controller/triggers"
	"github.com/gratefulagents/gratefulagents/internal/dashboard"
	"github.com/gratefulagents/gratefulagents/internal/githubapp"
	"github.com/gratefulagents/gratefulagents/internal/mode"
	"github.com/gratefulagents/gratefulagents/internal/oauthrefresh"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/contentblob"
	pgstore "github.com/gratefulagents/gratefulagents/internal/store/postgres"
	"github.com/gratefulagents/gratefulagents/rpc/auth/authconnect"
	"github.com/gratefulagents/gratefulagents/rpc/platform/platformconnect"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(agentsandboxv1alpha1.AddToScheme(scheme))
	utilruntime.Must(agentsandboxextensionsv1alpha1.AddToScheme(scheme))

	utilruntime.Must(platformv1alpha1.AddToScheme(scheme))
	utilruntime.Must(triggersv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var enableDashboard bool
	var enableAgentRunTeamMode bool
	var enableAgentRunOverseer bool
	var enableRepoMaintainer bool
	var dashboardAddr string
	var githubWebhookAddr string
	var allowUnauthenticatedWebhooks bool
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.BoolVar(&enableDashboard, "enable-dashboard", false,
		"If set, the ConnectRPC dashboard server will be started.")
	flag.BoolVar(&enableAgentRunTeamMode, "enable-agentrun-team-mode", false,
		"If set, the experimental AgentRun team-mode orchestration endpoints are exposed through the dashboard API.")
	flag.BoolVar(&enableAgentRunOverseer, "enable-agentrun-overseer", false,
		"If set, reconcile opt-in per-run overseers configured through AgentRun spec.overseer.")
	flag.BoolVar(&enableRepoMaintainer, "enable-repo-maintainer", false,
		"If set, reconcile opt-in repository maintainers configured through GitHubRepository spec.maintainer.")
	flag.StringVar(&dashboardAddr, "dashboard-addr", ":8090",
		"The address the dashboard server binds to.")
	flag.StringVar(&githubWebhookAddr, "github-webhook-addr", ":8091",
		"The address the GitHub webhook ingress binds to. Webhooks are served on "+
			"a dedicated listener so it can be exposed externally while the "+
			"dashboard stays internal. Set to empty to disable webhook ingress.")
	flag.BoolVar(&allowUnauthenticatedWebhooks, "allow-unauthenticated-webhooks", false,
		"If set, GitHub webhook ingress accepts unsigned deliveries when no webhook secret is configured.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	restConfig := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "cbb5f74f.gratefulagents.dev",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Shared Postgres state store for the AgentRun finalizer, dashboard, and
	// trigger controllers.
	var sharedStateStore store.StateStore
	var pgPool *pgxpool.Pool
	if dsn := strings.TrimSpace(os.Getenv("DATABASE_URL")); dsn != "" {
		s, err := pgstore.New(context.Background(), dsn)
		if err != nil {
			// Fail fast: continuing without the state store silently disables
			// ownership/sharing enforcement and message routing even though the
			// operator was explicitly configured to use Postgres.
			setupLog.Error(err, "failed to connect to Postgres — DATABASE_URL is set, refusing to start degraded")
			os.Exit(1)
		}
		pgPool = s.Pool()
		if err := pgstore.Migrate(context.Background(), pgPool); err != nil {
			setupLog.Error(err, "failed to run Postgres migrations")
			os.Exit(1)
		}
		contentBlobs, err := contentblob.NewS3FromEnv()
		if err != nil {
			setupLog.Error(err, "failed to configure S3 project asset storage")
			os.Exit(1)
		}
		s.SetProjectContentBlobStore(contentBlobs)
		reconcileCtx, cancelReconcile := context.WithTimeout(context.Background(), 30*time.Second)
		if err := s.ReconcileProjectContentBlobDeletions(reconcileCtx, 100); err != nil {
			setupLog.Error(err, "failed to reconcile pending project asset deletions")
		}
		cancelReconcile()
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := s.ReconcileProjectContentBlobDeletions(ctx, 100)
				cancel()
				if err != nil {
					setupLog.Error(err, "failed to reconcile pending project asset deletions")
				}
			}
		}()
		sharedStateStore = s
		setupLog.Info("Connected to Postgres for message routing (migrations applied)")
	} else {
		setupLog.Info("DATABASE_URL not set — will not write messages to Postgres")
	}

	if err := (&platformcontroller.AgentRunReconciler{
		Client:       mgr.GetClient(),
		ModeResolver: mode.NewResolver(mgr.GetClient()),
		StateStore:   sharedStateStore,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AgentRun")
		os.Exit(1)
	}

	if enableAgentRunOverseer {
		if err := (&platformcontroller.AgentRunOverseerReconciler{
			Client:     mgr.GetClient(),
			Scheme:     mgr.GetScheme(),
			StateStore: sharedStateStore,
			Recorder:   mgr.GetEventRecorderFor("agentrun-overseer"),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "AgentRunOverseer")
			os.Exit(1)
		}
	}

	if err := (&platformcontroller.MCPServerReconciler{
		Client: mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MCPServer")
		os.Exit(1)
	}

	if err := (&platformcontroller.SkillReconciler{
		Client: mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Skill")
		os.Exit(1)
	}

	if err := (&platformcontroller.RuntimeProfileReconciler{
		Client: mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "RuntimeProfile")
		os.Exit(1)
	}

	if err := (&triggercontroller.ProjectReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Project")
		os.Exit(1)
	}

	if err := (&triggercontroller.LinearProjectReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		StateStore: sharedStateStore,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "LinearProject")
		os.Exit(1)
	}

	gitHubAppMinter := githubapp.NewKeyedMinter()
	ghReconciler := &triggercontroller.GitHubRepositoryReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		StateStore:        sharedStateStore,
		GitHubAppMinter:   gitHubAppMinter,
		Recorder:          mgr.GetEventRecorderFor("githubrepository"),
		MaintainerEnabled: enableRepoMaintainer,
	}
	if err := ghReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "GitHubRepository")
		os.Exit(1)
	}

	// Autonomous PR review loop: reviews agent-created PRs and routes review
	// feedback back to the implementer run until approval.
	prLoopEngine := &triggercontroller.PRLoopEngine{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		StateStore:      sharedStateStore,
		Recorder:        mgr.GetEventRecorderFor("pr-loop"),
		GitHubAppMinter: gitHubAppMinter,
	}
	if err := (&triggercontroller.PRLoopReconciler{
		Client: mgr.GetClient(),
		Engine: prLoopEngine,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PRLoop")
		os.Exit(1)
	}
	if err := (&triggercontroller.PullRequestArtifactReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("pull-request-artifacts"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PullRequestArtifact")
		os.Exit(1)
	}
	if err := (&triggercontroller.PullRequestMonitorReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		Engine:          prLoopEngine,
		GitHubAppMinter: gitHubAppMinter,
		Recorder:        mgr.GetEventRecorderFor("pull-request-monitor"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PullRequestMonitor")
		os.Exit(1)
	}

	if err := (&triggercontroller.CronReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		StateStore: sharedStateStore,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Cron")
		os.Exit(1)
	}

	if err := (&triggercontroller.SlackAgentReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SlackAgent")
		os.Exit(1)
	}

	if err := (&triggercontroller.SlackWorkspaceReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SlackWorkspace")
		os.Exit(1)
	}

	// +kubebuilder:scaffold:builder

	// Leader-only background goroutine that refreshes OAuth tokens in K8s
	// Secrets before they expire. Pods and dashboard read-only.
	if err := mgr.Add(oauthrefresh.New(mgr.GetClient())); err != nil {
		setupLog.Error(err, "unable to add OAuth token refresher")
		os.Exit(1)
	}
	if err := mgr.Add(githubapp.NewRefresher(mgr.GetClient(), gitHubAppMinter, mgr.GetScheme())); err != nil {
		setupLog.Error(err, "unable to add GitHub App token refresher")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// GitHub webhook ingress on a dedicated listener, independent of the
	// dashboard: clusters can expose only this port externally (GitHub must
	// reach it) while the dashboard stays internal. Deliveries authenticate
	// via HMAC webhook secrets, not JWT.
	if githubWebhookAddr != "" {
		ghWebhook := &triggercontroller.GitHubWebhookHandler{
			Client:               mgr.GetClient(),
			Reconciler:           ghReconciler,
			Recorder:             mgr.GetEventRecorderFor("github-webhook"),
			AllowUnauthenticated: allowUnauthenticatedWebhooks,
			PRSink:               prLoopEngine,
		}
		appWebhook := &triggercontroller.GitHubAppWebhookHandler{
			Inner:                  ghWebhook,
			WebhookSecretNamespace: strings.TrimSpace(os.Getenv("POD_NAMESPACE")),
			WebhookSecretName:      strings.TrimSpace(os.Getenv("GITHUB_APP_WEBHOOK_SECRET")),
			AllowUnauthenticated:   allowUnauthenticatedWebhooks,
		}
		if allowUnauthenticatedWebhooks {
			setupLog.Info("WARNING: GitHub webhook ingress will accept unsigned deliveries when no webhook secret is configured")
		}
		webhookMux := http.NewServeMux()
		triggercontroller.RegisterGitHubWebhookRoutes(webhookMux, ghWebhook)
		triggercontroller.RegisterGitHubAppWebhookRoutes(webhookMux, appWebhook)
		webhookMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		if err := mgr.Add(&httpServerRunnable{
			name: "github-webhook-ingress",
			server: &http.Server{
				Addr:              githubWebhookAddr,
				Handler:           webhookMux,
				ReadHeaderTimeout: 10 * time.Second,
			},
		}); err != nil {
			setupLog.Error(err, "unable to add GitHub webhook ingress server")
			os.Exit(1)
		}
		setupLog.Info("GitHub webhook ingress enabled", "addr", githubWebhookAddr)
	}

	if enableDashboard {
		clientset, err := kubernetes.NewForConfig(restConfig)
		if err != nil {
			setupLog.Error(err, "unable to create kubernetes clientset for dashboard")
			os.Exit(1)
		}

		// Initialize integrated auth.
		var authStore auth.Store
		var jwksCache *auth.JWKSCache
		if pgPool != nil {
			authStore = auth.NewPGStore(pgPool)

			// Seed admin user via K8s secret.
			if err := auth.SeedAdmin(context.Background(), clientset, authStore); err != nil {
				setupLog.Error(err, "failed to seed admin user")
				os.Exit(1)
			}
		}

		var jwtIssuer *auth.JWTIssuer
		if keyPath := strings.TrimSpace(os.Getenv("JWT_PRIVATE_KEY_PATH")); keyPath != "" {
			jwtIssuer, err = auth.NewJWTIssuer(keyPath)
			if err != nil {
				setupLog.Error(err, "failed to create JWT issuer")
				os.Exit(1)
			}
		} else {
			// Persist the signing key in a Secret so sessions survive restarts
			// and all replicas verify each other's tokens.
			keyPEM, keyErr := auth.EnsureSigningKeyPEM(context.Background(), clientset)
			if keyErr != nil {
				setupLog.Error(keyErr, "failed to ensure persistent JWT signing key (set JWT_PRIVATE_KEY_PATH to bypass)")
				os.Exit(1)
			}
			jwtIssuer, err = auth.NewJWTIssuerFromPEM(keyPEM)
			if err != nil {
				setupLog.Error(err, "failed to create JWT issuer from persisted key")
				os.Exit(1)
			}
		}
		jwksCache, err = auth.NewJWKSCacheFromIssuer(jwtIssuer)
		if err != nil {
			setupLog.Error(err, "failed to create JWKS cache")
			os.Exit(1)
		}

		var googleVerifier *auth.GoogleVerifier
		googleClientID := os.Getenv("GOOGLE_CLIENT_ID")
		if googleClientID != "" {
			googleVerifier = auth.NewGoogleVerifier(googleClientID)
			setupLog.Info("Google OAuth enabled", "clientID", googleClientID)
		}

		roleResolver := auth.NewRoleResolver()
		authServer := auth.NewServer(authStore, googleVerifier, jwtIssuer, roleResolver)

		dashOpts := []dashboard.ServerOption{dashboard.WithAPIReader(mgr.GetAPIReader())}
		if sharedStateStore != nil {
			dashOpts = append(dashOpts, dashboard.WithStateStore(sharedStateStore))
		}
		if authStore != nil {
			dashOpts = append(dashOpts, dashboard.WithAuthStore(authStore))
		}
		githubAppIDRaw := strings.TrimSpace(os.Getenv("GITHUB_APP_ID"))
		githubAppSlug := strings.TrimSpace(os.Getenv("GITHUB_APP_SLUG"))
		githubAppPrivateKeySecret := strings.TrimSpace(os.Getenv("GITHUB_APP_PRIVATE_KEY_SECRET"))
		if githubAppIDRaw != "" || githubAppSlug != "" || githubAppPrivateKeySecret != "" {
			var githubAppID int64
			if githubAppIDRaw != "" {
				var err error
				githubAppID, err = strconv.ParseInt(githubAppIDRaw, 10, 64)
				if err != nil {
					setupLog.Error(err, "invalid GITHUB_APP_ID")
					os.Exit(1)
				}
			}
			dashOpts = append(dashOpts, dashboard.WithGitHubAppConfig(githubAppID, githubAppSlug, githubAppPrivateKeySecret, strings.TrimSpace(os.Getenv("POD_NAMESPACE"))))
		}
		srv := dashboard.NewServer(mgr.GetClient(), mgr.GetScheme(), clientset, restConfig, enableAgentRunTeamMode, dashOpts...)
		if err := mgr.Add(dashboard.NewConnectionSecretGarbageCollector(mgr.GetClient(), mgr.GetAPIReader())); err != nil {
			setupLog.Error(err, "unable to add connection Secret garbage collector")
			os.Exit(1)
		}
		handler := dashboard.NewPlatformServiceConnectHandler(srv)

		r := chi.NewRouter()
		// Access logging first so every request is visible — including CORS
		// preflights and auth-middleware rejections that never reach an RPC.
		r.Use(dashboard.AccessLogMiddleware())
		r.Use(cors.Handler(cors.Options{
			AllowedOrigins:   []string{"*"},
			AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
			AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "Connect-Protocol-Version", "Connect-Accept-Encoding", "Connect-Content-Encoding"},
			ExposedHeaders:   []string{"Grpc-Status", "Grpc-Message", "Connect-Content-Encoding"},
			AllowCredentials: false,
			MaxAge:           86400,
		}))
		// HTTP-level compression for RPC and static responses. connect-web
		// never negotiates Connect-Accept-Encoding, so streaming frames are
		// only compressed here. chi's compress writer passes Flush() through
		// to the gzip writer and underlying ResponseWriter, so streaming RPC
		// frames still flush per write, and it skips responses that already
		// carry Content-Encoding (connect-go unary gzip).
		r.Use(chimiddleware.Compress(5,
			"application/json",
			"application/connect+json",
			"text/html",
			"text/css",
			"application/javascript",
			"text/javascript",
			"image/svg+xml",
			"text/plain",
		))

		// Public routes used while native clients bootstrap (no auth required).
		r.Get("/api/config", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"authEnabled":    true,
				"googleClientId": googleClientID,
			})
		})
		r.Get("/api/version", dashboard.VersionHandler(buildinfo.Version))

		// Auth RPCs — public mounting (Login/Refresh must work without a
		// token), with optional JWT parsing so identity-aware handlers like
		// GetCurrentUser and SearchUsers can authenticate the caller.
		authPath, authHandler := authconnect.NewAuthServiceHandler(
			authServer,
			connect.WithInterceptors(dashboard.LoggingInterceptor()),
		)
		r.Group(func(r chi.Router) {
			r.Use(auth.OptionalJWTMiddleware(jwksCache, authStore))
			r.Mount(authPath, authHandler)
		})

		// Protected ConnectRPC routes — JWT middleware required. Actor
		// extraction runs outermost so the logging interceptor can attribute
		// every request; logging wraps all handlers so each RPC's outcome,
		// duration, and error code are visible server-side.
		connectOpts := connect.WithInterceptors(
			dashboard.RequestActorInterceptor(),
			dashboard.LoggingInterceptor(),
		)
		// Raise Connect's default (~4 MiB) read limit so project content
		// uploads up to the advertised 25 MiB policy fit with protobuf
		// framing overhead.
		readLimit := connect.WithReadMaxBytes(dashboard.MaxRPCReadBytes)
		r.Group(func(r chi.Router) {
			r.Use(auth.JWTMiddleware(jwksCache, authStore))
			path, h := platformconnect.NewPlatformServiceHandler(handler, connectOpts, readLimit)
			r.Mount(path, h)
		})

		// Serve the web UI (if DASHBOARD_WEB_DIST is set) for all non-API routes
		r.Handle("/*", dashboard.StaticHandler())

		httpServer := &http.Server{
			Addr: dashboardAddr,
			// h2c allows HTTP/2 without TLS so streaming RPCs multiplex over
			// one connection behind TLS-terminating proxies.
			Handler:           h2c.NewHandler(r, &http2.Server{}),
			WriteTimeout:      0, // Disabled for streaming RPCs
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		}

		if err := mgr.Add(&httpServerRunnable{name: "dashboard", server: httpServer}); err != nil {
			setupLog.Error(err, "unable to add dashboard server")
			os.Exit(1)
		}
		setupLog.Info("dashboard server enabled", "addr", dashboardAddr)

		if pprofAddr := strings.TrimSpace(os.Getenv("DASHBOARD_PPROF_ADDR")); pprofAddr != "" {
			pprofMux := http.NewServeMux()
			pprofMux.HandleFunc("/debug/pprof/", pprof.Index)
			pprofMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
			pprofMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
			pprofMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
			pprofMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
			pprofServer := &http.Server{
				Addr:              pprofAddr,
				Handler:           pprofMux,
				ReadHeaderTimeout: 10 * time.Second,
			}
			if err := mgr.Add(&httpServerRunnable{name: "pprof", server: pprofServer}); err != nil {
				setupLog.Error(err, "unable to add pprof server")
				os.Exit(1)
			}
			setupLog.Info("pprof server enabled", "addr", pprofAddr)
		}
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// httpServerRunnable wraps an HTTP server as a controller-runtime Runnable.
type httpServerRunnable struct {
	name   string
	server *http.Server
}

func (d *httpServerRunnable) Start(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := d.server.Shutdown(shutdownCtx); err != nil {
			setupLog.Error(err, "http server shutdown error", "server", d.name)
		}
	}()
	if err := d.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
