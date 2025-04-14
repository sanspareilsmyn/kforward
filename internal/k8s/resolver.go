// internal/k8s/resolver.go
package k8s

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ServiceIdentifier uniquely identifies a Kubernetes service.
type ServiceIdentifier struct {
	Namespace string
	Name      string
}

// TargetResolver defines the interface for determining which services to forward.
type TargetResolver interface {
	Resolve(ctx context.Context) ([]ServiceIdentifier, string, error)
}

// NamespaceResolver finds all services within a specific namespace.
type NamespaceResolver struct {
	k8sClient *Client
	namespace string
	logger    *zap.SugaredLogger
}

// NewNamespaceResolver creates a resolver for a namespace.
func NewNamespaceResolver(k8sClient *Client, namespace string) *NamespaceResolver {
	return &NamespaceResolver{
		k8sClient: k8sClient,
		namespace: namespace,
		logger:    zap.S().With("resolver", "namespace", "namespace", namespace),
	}
}

// Resolve lists all services in the configured namespace.
func (r *NamespaceResolver) Resolve(ctx context.Context) ([]ServiceIdentifier, string, error) {
	targetScope := fmt.Sprintf("namespace '%s'", r.namespace)
	r.logger.Info("Resolving targets for namespace")

	serviceList, err := r.k8sClient.Clientset().CoreV1().Services(r.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		r.logger.Errorw("Failed to list services in namespace", "error", err)
		return nil, targetScope, fmt.Errorf("failed to list services in namespace '%s': %w", r.namespace, err)
	}

	identifiers := make([]ServiceIdentifier, 0, len(serviceList.Items))
	for _, svc := range serviceList.Items {
		identifiers = append(identifiers, ServiceIdentifier{
			Namespace: svc.Namespace,
			Name:      svc.Name,
		})
	}

	r.logger.Infow("Resolved target services from namespace", "count", len(identifiers))
	return identifiers, targetScope, nil
}

// ExplicitServiceResolver resolves targets from a list of "ns/name" strings.
type ExplicitServiceResolver struct {
	serviceArgs []string
	logger      *zap.SugaredLogger
}

func NewExplicitServiceResolver(serviceArgs []string) *ExplicitServiceResolver {
	return &ExplicitServiceResolver{
		serviceArgs: serviceArgs,
		logger:      zap.S().With("resolver", "explicit"),
	}
}

// Resolve parses the service arguments into ServiceIdentifiers.
func (r *ExplicitServiceResolver) Resolve(ctx context.Context) ([]ServiceIdentifier, string, error) {
	targetScope := fmt.Sprintf("services %v", r.serviceArgs)
	r.logger.Infow("Resolving targets from explicit arguments", "args", r.serviceArgs)

	identifiers := make([]ServiceIdentifier, 0, len(r.serviceArgs))
	var validationErrors []error

	processed := make(map[string]struct{}) // Track unique ns/name pairs

	for _, arg := range r.serviceArgs {
		parts := strings.SplitN(arg, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			err := fmt.Errorf("invalid service format '%s', use 'namespace/service-name'", arg)
			r.logger.Errorw("Invalid service format in argument", "arg", arg, "error", err)
			validationErrors = append(validationErrors, err)
			continue
		}

		id := ServiceIdentifier{
			Namespace: parts[0],
			Name:      parts[1],
		}
		idKey := fmt.Sprintf("%s/%s", id.Namespace, id.Name)

		if _, exists := processed[idKey]; exists {
			r.logger.Debugw("Duplicate service argument skipped", "arg", arg)
			continue
		}

		identifiers = append(identifiers, id)
		processed[idKey] = struct{}{}
	}

	if len(validationErrors) > 0 {
		r.logger.Errorw("Errors encountered during explicit service resolution", "errorCount", len(validationErrors))
		return nil, targetScope, fmt.Errorf("failed to validate all explicit services: %w", validationErrors[0])
	}

	r.logger.Infow("Resolved target services from explicit arguments", "count", len(identifiers))
	return identifiers, targetScope, nil
}
