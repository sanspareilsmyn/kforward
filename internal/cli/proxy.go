package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/sanspareilsmyn/kforward/internal/k8s"
	"github.com/sanspareilsmyn/kforward/internal/manager"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/sanspareilsmyn/kforward/internal/proxy"
)

// Flag variables
var (
	proxyNamespace string
	proxyServices  []string
	proxyPort      int
)

// newProxyCmd creates the 'proxy' command using the Manager and Proxy approach.
func newProxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Starts a local proxy routing traffic via 'kubectl port-forward'",
		Long: `Starts a local HTTP/HTTPS proxy server. When a request targeting a Kubernetes service
(e.g., my-service.my-namespace.svc.cluster.local) is received, kforward automatically
runs 'kubectl port-forward' for that service in the background (if not already running)
and forwards the request through it.

Specify the scope of services to manage using EITHER --namespace OR --service flags.
Use --context to specify the Kubernetes context if needed (--context is persistent).

Configure your client (e.g., browser, curl) to use this proxy:
export HTTP_PROXY=http://localhost:<port>
export HTTPS_PROXY=http://localhost:<port>`,
		RunE: runProxy,
	}

	cmd.Flags().StringVarP(&proxyNamespace, "namespace", "n", "", "Kubernetes namespace to manage forwards for (mutually exclusive with --service)")
	cmd.Flags().StringSliceVarP(&proxyServices, "service", "s", []string{}, "Specific Kubernetes service(s) to manage forwards for ('namespace/service-name' format; mutually exclusive with --namespace)")
	cmd.Flags().IntVarP(&proxyPort, "port", "p", 1080, "Local port for the kforward HTTP/HTTPS proxy server")

	return cmd
}

// runProxy is the main logic for the proxy command, called by Cobra's RunE.
func runProxy(cmd *cobra.Command, args []string) error {
	logger := zap.S()

	// 1. Validate Flags & Prerequisites
	if err := validateProxyFlags(); err != nil {
		return err
	}
	if err := checkKubectl(); err != nil {
		return err
	}

	// 2. Setup Context and Defer Cleanup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 3. Initialize K8s Client and Manager
	k8sClient, pfManager, err := initializeK8sComponents(ctx, kubeContext)
	if err != nil {
		return err
	}
	defer pfManager.StopAll()

	// 4. Start Initial kubectl Port-Forwards
	targetScope, err := startInitialForwards(ctx, pfManager, k8sClient, proxyNamespace, proxyServices)
	if err != nil {
		logger.Warnw("Errors occurred during initial port forward setup, continuing...", "error", err)
		if proxyNamespace != "" {
			targetScope = fmt.Sprintf("namespace '%s'", proxyNamespace)
		} else {
			targetScope = fmt.Sprintf("services %v", proxyServices)
		}
	}
	logger.Infow("Initial port forward setup complete.", "targetScope", targetScope)

	// 5. Initialize HTTP Proxy Server
	logger.Info("Initializing HTTP proxy server...")
	httpProxy, err := proxy.NewHTTPServer(pfManager, proxyPort)
	if err != nil {
		logger.Errorw("Failed to initialize HTTP proxy server", "error", err)
		return fmt.Errorf("failed to initialize HTTP proxy server: %w", err)
	}
	logger.Infow("HTTP proxy server initialized.", "port", proxyPort)

	// 6. Run Server and Wait for Shutdown ---
	return runServerAndWaitForShutdown(ctx, cancel, httpProxy, pfManager, proxyPort, targetScope)
}

// validateProxyFlags checks if the provided flags are valid and consistent.
func validateProxyFlags() error {
	if proxyNamespace != "" && len(proxyServices) > 0 {
		return errors.New("flags --namespace and --service are mutually exclusive")
	}
	if proxyNamespace == "" && len(proxyServices) == 0 {
		return errors.New("either --namespace or --service flag must be specified")
	}
	return nil
}

// checkKubectl verifies if the kubectl command is available in the system's PATH.
func checkKubectl() error {
	logger := zap.S()
	if _, err := exec.LookPath("kubectl"); err != nil {
		logger.Error("kforward requires kubectl to be installed and accessible via PATH.")
		return fmt.Errorf("'kubectl' command not found in PATH: %w", err)
	}
	return nil
}

// initializeK8sComponents sets up the Kubernetes client and the port-forward manager.
func initializeK8sComponents(ctx context.Context, kctx string) (*k8s.Client, *manager.Manager, error) {
	logger := zap.S()

	logger.Info("Initializing Kubernetes client...")
	k8sClient, err := k8s.NewClient(kctx)
	if err != nil {
		logger.Errorw("Failed to initialize Kubernetes client", "context", kctx, "error", err)
		return nil, nil, fmt.Errorf("failed to initialize Kubernetes client: %w", err)
	}
	logger.Info("Kubernetes client initialized.")

	// Test connection
	serverVersion, err := k8sClient.Clientset().Discovery().ServerVersion()
	if err != nil {
		usedCtx := kctx
		if usedCtx == "" {
			usedCtx = "current-context"
		}
		logger.Errorw("Failed to connect to Kubernetes cluster", "context", usedCtx, "error", err)
		return nil, nil, fmt.Errorf("failed to connect to Kubernetes cluster (context: '%s'): %w", usedCtx, err)
	}
	logger.Infow("Successfully connected to Kubernetes cluster.", "version", serverVersion.GitVersion)

	logger.Info("Initializing port forward manager...")
	pfManager := manager.NewManager(k8sClient, kctx)
	logger.Info("Port forward manager initialized.")

	return k8sClient, pfManager, nil
}

func startInitialForwards(ctx context.Context, pfManager *manager.Manager, k8sClient *k8s.Client, namespaceFlag string, serviceFlags []string) (string, error) {
	logger := zap.S()
	var strategy ForwardingStrategy

	// 1. Select the Strategy based on flags
	if namespaceFlag != "" {
		logger.Info("Selecting Namespace Forwarding Strategy")
		strategy = newNamespaceStrategy(k8sClient, pfManager, namespaceFlag)
	} else {
		logger.Info("Selecting Explicit Service Forwarding Strategy")
		strategy = newExplicitServiceStrategy(k8sClient, pfManager, serviceFlags)
	}

	// 2. Execute the selected strategy
	logger.Info("Executing forwarding strategy...")
	targetScope, err := strategy.SetupForwards(ctx)

	// 3. Log outcome
	if err != nil {
		logger.Errorw("Forwarding strategy execution failed", "targetScope", targetScope, "error", err)
	} else {
		logger.Infow("Forwarding strategy execution completed successfully", "targetScope", targetScope)
	}

	return targetScope, err
}

// initiateForwardsForIdentifiers handles the core logic of fetching service details
// and starting the actual port-forwards for a given list of service identifiers.
func initiateForwardsForIdentifiers(ctx context.Context, pfManager *manager.Manager, k8sClient *k8s.Client, identifiers []k8s.ServiceIdentifier) error {
	logger := zap.S().With("phase", "initiation")
	var setupErrors []error

	logger.Infow("Starting forward initiation", "serviceCount", len(identifiers))

	for _, id := range identifiers {
		serviceLogger := logger.With("namespace", id.Namespace, "service", id.Name)

		// 1. Get full service details (needed for ports) - Use retry helper
		svc, err := getServiceWithRetries(ctx, k8sClient, id.Namespace, id.Name, serviceLogger)
		if err != nil {
			serviceLogger.Errorw("Could not get service details, skipping.", "error", err)
			setupErrors = append(setupErrors, fmt.Errorf("get service %s/%s: %w", id.Namespace, id.Name, err))
			continue
		}

		// 2. Check for ports
		if len(svc.Spec.Ports) == 0 {
			serviceLogger.Warnw("Service has no ports defined, skipping forward.")
			continue // Skip this service identifier
		}
		portNumbers := make([]int32, len(svc.Spec.Ports))
		for i, p := range svc.Spec.Ports {
			portNumbers[i] = p.Port
		}
		serviceLogger.Debugw("Found service ports", "ports", portNumbers)

		// 3. Start forwarding for all ports
		portErrorCount := 0
		for _, port := range svc.Spec.Ports {
			portLogger := serviceLogger.With("port", port.Port)
			portLogger.Info("Attempting to start port forward")
			err = pfManager.StartForwardingForService(ctx, id.Namespace, id.Name, int(port.Port))
			if err != nil {
				setupErrors = append(setupErrors, fmt.Errorf("start forward %s/%s:%d: %w", id.Namespace, id.Name, port.Port, err))
				portErrorCount++
			}
		}
		if portErrorCount > 0 {
			serviceLogger.Warnw("Encountered errors starting forwards for service ports", "errorCount", portErrorCount)
		}
	}

	if len(setupErrors) > 0 {
		logger.Warnw("Completed forward initiation with errors", "errorCount", len(setupErrors))
		return fmt.Errorf("encountered %d error(s) during forward initiation (showing first): %w", len(setupErrors), setupErrors[0])
	}

	logger.Info("Completed forward initiation successfully.")
	return nil
}

func getServiceWithRetries(ctx context.Context, k8sClient *k8s.Client, ns string, svcName string, logger *zap.SugaredLogger) (*corev1.Service, error) {
	var svc *corev1.Service
	var err error
	retries := 0
	maxRetries := 2

	for retries <= maxRetries {
		svc, err = k8sClient.GetService(ctx, ns, svcName)
		if err == nil {
			return svc, nil // Success
		}

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			logger.Warnw("Context cancelled or deadline exceeded while getting service", "error", err)
			return nil, err
		}

		logger.Warnw("Failed to get service, retrying...", "attempt", retries+1, "maxAttempts", maxRetries+1, "error", err)
		retries++
		if retries <= maxRetries {
			time.Sleep(time.Duration(retries) * 500 * time.Millisecond)
		}
	}

	logger.Errorw("Could not get service after retries.", "error", err)
	return nil, fmt.Errorf("failed after %d retries: %w", maxRetries+1, err)
}

// runServerAndWaitForShutdown starts the HTTP proxy and blocks until a shutdown signal
// (SIGINT, SIGTERM) or a proxy error occurs. It then orchestrates graceful shutdown.
func runServerAndWaitForShutdown(ctx context.Context, cancel context.CancelFunc, httpProxy *proxy.Server, pfManager *manager.Manager, port int, targetScope string) error {
	logger := zap.S()

	// Channel to receive error from the proxy server goroutine
	proxyErrChan := make(chan error, 1)
	go func() {
		logger.Infow("Starting HTTP proxy server", "address", fmt.Sprintf("127.0.0.1:%d", port))
		if err := httpProxy.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Errorw("HTTP proxy server error", "error", err)
			proxyErrChan <- fmt.Errorf("proxy server failed: %w", err)
		} else {
			logger.Info("HTTP proxy server stopped listening.")
			proxyErrChan <- nil
		}
	}()

	// 1. Log Ready Status
	logger.Infof("kforward (using kubectl backend) is now proxying requests for %s", targetScope)
	logger.Info("Proxy server running on port:", port)
	logger.Info("Configure your client (e.g., export http_proxy=http://localhost:" + fmt.Sprintf("%d", port) + " https_proxy=http://localhost:" + fmt.Sprintf("%d", port) + ")")
	logger.Info("Press Ctrl+C to stop.")

	// 2. Wait for Shutdown Signal or Proxy Error
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	var exitError error

	select {
	case sig := <-sigChan:
		logger.Infow("Received signal, shutting down gracefully...", "signal", sig)
		performGracefulShutdown(cancel, pfManager, httpProxy)
		err := <-proxyErrChan
		if err != nil {
			logger.Warnw("Proxy server reported error during signal shutdown", "error", err)
		}
		logger.Info("Signal shutdown complete.")

	case err := <-proxyErrChan:
		if err != nil {
			logger.Errorw("Proxy server stopped unexpectedly due to error", "error", err)
			exitError = err
			logger.Info("Initiating cleanup after proxy error...")
			performGracefulShutdown(cancel, pfManager, httpProxy)
		} else {
			logger.Info("Proxy server stopped cleanly (before signal or explicit stop).")
			performGracefulShutdown(cancel, pfManager, httpProxy)
		}
	}

	logger.Info("kforward proxy command finished.")
	return exitError
}

// performGracefulShutdown orchestrates the shutdown of components.
func performGracefulShutdown(cancel context.CancelFunc, pfManager *manager.Manager, httpProxy *proxy.Server) {
	logger := zap.S()
	logger.Info("Starting graceful shutdown...")

	// 1. Cancel the main context (signals background tasks like manager's potential listeners)
	cancel()

	// 2. Shutdown the HTTP proxy server first to stop accepting new connections
	shutdownTimeout := 15 * time.Second
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	logger.Infow("Shutting down HTTP proxy server...", "timeout", shutdownTimeout)
	if err := httpProxy.Shutdown(shutdownCtx); err != nil {
		logger.Errorw("Error during HTTP proxy server shutdown", "error", err)
	} else {
		logger.Info("HTTP proxy server shut down successfully.")
	}

	// 3. Stop all kubectl port-forward processes managed by the manager
	logger.Info("Stopping all managed kubectl port-forward processes...")
	pfManager.StopAll()
	logger.Info("All kubectl port-forward processes stopped.")

	logger.Info("Graceful shutdown sequence finished.")
}
