package cli

import (
	"github.com/spf13/cobra"
)

var kubeContext string

// NewRootCmd creates and configures the root 'kforward' command.
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "kforward",
		Short: "A lightweight proxy for seamless local development against Kubernetes services",
		Long: `kforward is a simple CLI tool that helps you access services
running inside your Kubernetes cluster using their standard service names,
eliminating the need to manage multiple 'kubectl port-forward' sessions.`,
	}

	rootCmd.PersistentFlags().StringVar(&kubeContext, "context", "", "Kubernetes context to use (overrides current-context in kubeconfig)")
	rootCmd.AddCommand(newProxyCmd())
	rootCmd.AddCommand(newStatusCmd())

	return rootCmd
}
