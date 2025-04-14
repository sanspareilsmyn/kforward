package cli

import (
	"context"
	"fmt"

	"github.com/sanspareilsmyn/kforward/internal/k8s" // Need k8s types and resolvers
	"github.com/sanspareilsmyn/kforward/internal/manager"
	"go.uber.org/zap"
)

// ForwardingStrategy defines the interface for setting up initial port forwards
// based on different modes (namespace, explicit services, etc.).
type ForwardingStrategy interface {
	SetupForwards(ctx context.Context) (string, error)
}

type namespaceStrategy struct {
	k8sClient *k8s.Client
	pfManager *manager.Manager
	namespace string
	logger    *zap.SugaredLogger
}

func newNamespaceStrategy(k8sClient *k8s.Client, pfManager *manager.Manager, namespace string) ForwardingStrategy {
	return &namespaceStrategy{
		k8sClient: k8sClient,
		pfManager: pfManager,
		namespace: namespace,
		logger:    zap.S().With("strategy", "namespace", "namespace", namespace),
	}
}

func (s *namespaceStrategy) SetupForwards(ctx context.Context) (string, error) {
	s.logger.Info("Executing namespace forwarding strategy")

	// 1. Resolve targets using NamespaceResolver
	resolver := k8s.NewNamespaceResolver(s.k8sClient, s.namespace)
	identifiers, targetScope, err := resolver.Resolve(ctx)
	if err != nil {
		s.logger.Errorw("Namespace resolution failed", "error", err)
		return targetScope, fmt.Errorf("namespace resolution failed: %w", err)
	}
	if len(identifiers) == 0 {
		s.logger.Warnw("Namespace resolver returned no target services.", "targetScope", targetScope)
		return targetScope, nil // Not an error, but nothing to do
	}
	s.logger.Infow("Namespace resolution successful", "count", len(identifiers))

	// 2. Initiate forwards for resolved identifiers
	initiationErr := initiateForwardsForIdentifiers(ctx, s.pfManager, s.k8sClient, identifiers)
	if initiationErr != nil {
		s.logger.Warnw("Forward initiation failed", "error", initiationErr)
		return targetScope, initiationErr
	}

	s.logger.Info("Namespace forwarding strategy completed successfully")
	return targetScope, nil
}

type explicitServiceStrategy struct {
	k8sClient *k8s.Client
	pfManager *manager.Manager
	services  []string
	logger    *zap.SugaredLogger
}

func newExplicitServiceStrategy(k8sClient *k8s.Client, pfManager *manager.Manager, services []string) ForwardingStrategy {
	return &explicitServiceStrategy{
		k8sClient: k8sClient,
		pfManager: pfManager,
		services:  services,
		logger:    zap.S().With("strategy", "explicit_services"),
	}
}

func (s *explicitServiceStrategy) SetupForwards(ctx context.Context) (string, error) {
	s.logger.Info("Executing explicit service forwarding strategy")

	// 1. Resolve targets using ExplicitServiceResolver
	resolver := k8s.NewExplicitServiceResolver(s.services)
	identifiers, targetScope, err := resolver.Resolve(ctx)
	if err != nil {
		s.logger.Errorw("Explicit service resolution failed", "error", err)
		return targetScope, fmt.Errorf("explicit service resolution failed: %w", err)
	}
	if len(identifiers) == 0 {
		s.logger.Warnw("Explicit service resolver returned no target services.", "targetScope", targetScope)
		return targetScope, nil // Not an error, but nothing to do
	}
	s.logger.Infow("Explicit service resolution successful", "count", len(identifiers))

	// 2. Initiate forwards for resolved identifiers (using the shared logic)
	initiationErr := initiateForwardsForIdentifiers(ctx, s.pfManager, s.k8sClient, identifiers)
	if initiationErr != nil {
		s.logger.Warnw("Forward initiation failed", "error", initiationErr)
		return targetScope, initiationErr
	}

	s.logger.Info("Explicit service forwarding strategy completed successfully")
	return targetScope, nil
}
