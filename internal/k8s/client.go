package k8s

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Client holds the necessary Kubernetes client objects.
type Client struct {
	clientset kubernetes.Interface
	config    *rest.Config
	logger    *zap.SugaredLogger
}

// NewClient creates a new Kubernetes client using kubeconfig, allowing context override.
func NewClient(contextOverride string) (*Client, error) {
	logger := zap.S()
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if len(loadingRules.ExplicitPath) > 0 {
		logger.Infow("Attempting to use KUBECONFIG environment variable", "path", loadingRules.ExplicitPath)
	}

	configOverrides := &clientcmd.ConfigOverrides{
		CurrentContext: contextOverride,
	}

	// Build configuration from flags, env vars, and default paths.
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	if err != nil {
		logger.Warnw("Could not load kubeconfig, attempting in-cluster config...", "error", err)
		config, err = rest.InClusterConfig()
		if err != nil {
			logger.Errorw("Failed to load Kubernetes config (tried kubeconfig and in-cluster)", "error", err)
			return nil, fmt.Errorf("failed to load any Kubernetes config (kubeconfig/in-cluster): %w", err)
		}
		logger.Info("Using in-cluster Kubernetes configuration.")
	} else {
		logger.Info("Using Kubernetes configuration from kubeconfig.")
		if contextOverride != "" {
			logger.Infow("Using provided context override", "context", contextOverride)
		}
	}

	// Create the clientset using the obtained config
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		logger.Errorw("Failed to create Kubernetes clientset", "error", err)
		return nil, fmt.Errorf("failed to create Kubernetes clientset: %w", err)
	}

	clientLogger := logger.With("component", "k8s-client")

	return &Client{
		clientset: clientset,
		config:    config,
		logger:    clientLogger,
	}, nil
}

// GetService retrieves a specific Kubernetes Service object.
func (c *Client) GetService(ctx context.Context, namespace, name string) (*corev1.Service, error) {
	c.logger.Debugw("Getting service", "namespace", namespace, "name", name)
	service, err := c.clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			c.logger.Warnw("Service not found", "namespace", namespace, "name", name)
		} else {
			c.logger.Errorw("Failed to get service", "namespace", namespace, "name", name, "error", err)
		}
		return nil, fmt.Errorf("get service '%s/%s': %w", namespace, name, err) // Wrap error
	}
	return service, nil
}

// GetEndpoints retrieves the Endpoints object associated with a specific service name.
func (c *Client) GetEndpoints(ctx context.Context, namespace, serviceName string) (*corev1.Endpoints, error) {
	c.logger.Debugw("Getting endpoints", "namespace", namespace, "service", serviceName)
	endpoints, err := c.clientset.CoreV1().Endpoints(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			c.logger.Infow("Endpoints not found", "namespace", namespace, "service", serviceName)
		} else {
			c.logger.Errorw("Failed to get endpoints", "namespace", namespace, "service", serviceName, "error", err)
		}
		return nil, fmt.Errorf("get endpoints for service '%s/%s': %w", namespace, serviceName, err)
	}
	return endpoints, nil
}

// ListServices retrieves all Service objects in a given namespace.
func (c *Client) ListServices(ctx context.Context, namespace string) ([]corev1.Service, error) {
	c.logger.Debugw("Listing services", "namespace", namespace)
	serviceList, err := c.clientset.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		c.logger.Errorw("Failed to list services", "namespace", namespace, "error", err)
		return nil, fmt.Errorf("list services in namespace '%s': %w", namespace, err)
	}
	items := make([]corev1.Service, 0, len(serviceList.Items))
	items = append(items, serviceList.Items...)
	return items, nil
}

// Config returns the underlying REST configuration. Useful for port-forwarding setup.
func (c *Client) Config() *rest.Config {
	return c.config
}

// Clientset returns the standard Kubernetes clientset interface.
func (c *Client) Clientset() kubernetes.Interface {
	return c.clientset
}

// FindReadyPodForServicePort finds a suitable pod name and its target port.
func FindReadyPodForServicePort(ctx context.Context, c *Client, namespace, serviceName string, servicePort int) (string, int, error) {
	logger := c.logger.With("operation", "find-ready-pod", "namespace", namespace, "service", serviceName, "servicePort", servicePort)
	logger.Debug("Attempting to find ready pod for service port")

	svc, err := c.GetService(ctx, namespace, serviceName)
	if err != nil {
		return "", 0, fmt.Errorf("cannot find service '%s/%s' to map port %d: %w", namespace, serviceName, servicePort, err)
	}

	targetPort, err := FindTargetPort(svc, servicePort, logger)
	if err != nil {
		return "", 0, fmt.Errorf("cannot resolve target port for service '%s/%s' port %d: %w", namespace, serviceName, servicePort, err)
	}
	logger.Debugw("Resolved service port to target port", "targetPort", targetPort)

	endpoints, err := c.GetEndpoints(ctx, namespace, serviceName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Warnw("No endpoints found for service, cannot find ready pod", "namespace", namespace, "service", serviceName)
			return "", 0, fmt.Errorf("no endpoints found for service '%s/%s'", namespace, serviceName)
		}
		return "", 0, fmt.Errorf("cannot find endpoints for service '%s/%s': %w", namespace, serviceName, err)
	}

	if len(endpoints.Subsets) == 0 {
		logger.Warn("Service has no endpoint subsets, indicating no ready pods.")
		return "", 0, fmt.Errorf("no ready pods found (no endpoint subsets) for service '%s/%s'", namespace, serviceName)
	}

	for i, subset := range endpoints.Subsets {
		subsetLogger := logger.With("subsetIndex", i)
		portMatchFound := false
		for _, epPort := range subset.Ports {
			if epPort.Port == int32(targetPort) {
				portMatchFound = true
				break
			}
		}
		if !portMatchFound {
			subsetLogger.Debugw("Endpoint subset does not expose target port", "targetPort", targetPort)
			continue
		}
		subsetLogger.Debugw("Endpoint subset exposes target port", "targetPort", targetPort)

		if len(subset.Addresses) > 0 {
			subsetLogger.Debugw("Checking ready addresses in subset", "count", len(subset.Addresses))
			address := subset.Addresses[0]
			if address.TargetRef != nil && address.TargetRef.Kind == "Pod" && address.TargetRef.Name != "" {
				podName := address.TargetRef.Name
				logger.Infow("Selected ready pod from endpoint subset", "podName", podName, "podIP", address.IP, "targetPort", targetPort)
				return podName, targetPort, nil
			} else {
				subsetLogger.Warnw("Endpoint address has IP but no Pod TargetRef, cannot use for port-forward", "addressIP", address.IP)
			}
		} else {
			subsetLogger.Debug("Endpoint subset has no ready addresses")
		}
	}

	logger.Warnw("No suitable ready pod found exposing target port in any endpoint subset", "targetPort", targetPort)
	return "", 0, fmt.Errorf("no ready pod found exposing target port %d for service '%s/%s'", targetPort, namespace, serviceName)
}

// FindTargetPort extracts the numeric target port for a given service port.
func FindTargetPort(svc *corev1.Service, servicePort int, logger *zap.SugaredLogger) (int, error) {
	foundServicePortSpec := false
	var targetPort int32 = -1

	for _, sp := range svc.Spec.Ports {
		if sp.Port == int32(servicePort) {
			foundServicePortSpec = true
			if sp.TargetPort.Type == 0 || sp.TargetPort.IntVal != 0 {
				targetPort = sp.TargetPort.IntVal
				if targetPort == 0 {
					targetPort = sp.Port
				}
			} else {
				logger.Errorw("Named TargetPort found, which is not supported by this tool", "namedPort", sp.TargetPort.StrVal)
				return 0, fmt.Errorf("named TargetPort '%s' not supported", sp.TargetPort.StrVal)
			}
			break
		}
	}

	if !foundServicePortSpec {
		logger.Warnw("Specified service port not found in service definition", "servicePort", servicePort)
		return 0, fmt.Errorf("port %d not defined in service spec", servicePort)
	}
	if targetPort <= 0 {
		logger.Errorw("Invalid target port resolved", "servicePort", servicePort, "resolvedTargetPort", targetPort)
		return 0, fmt.Errorf("invalid target port %d resolved for service port %d", targetPort, servicePort)
	}

	return int(targetPort), nil
}
