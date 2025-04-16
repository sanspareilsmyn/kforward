package cli

import (
	"encoding/json"
	"fmt"
	"github.com/sanspareilsmyn/kforward/internal/manager"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// Flag variable
var (
	statusAdminPort int
)

// newStatusCmd creates the 'status' command.
func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Checks the status of active forwards managed by a running kforward proxy",
		Long: `Connects to a running 'kforward proxy' instance via its admin port
and displays the current service-to-local-port mappings being managed.

The 'kforward proxy' command must be running with an active admin server
for this command to retrieve status information.`,
		RunE: runStatus,
	}

	cmd.Flags().IntVar(&statusAdminPort, "admin-port", 1081, "The local port where the kforward proxy's admin server is listening")

	return cmd
}

// runStatus is the logic for the status command.
func runStatus(cmd *cobra.Command, args []string) error {
	logger := zap.S()
	logger.Debugw("Running status command", "adminPort", statusAdminPort)

	// Construct the URL for the status endpoint
	statusURL := fmt.Sprintf("http://127.0.0.1:%d/status", statusAdminPort)
	logger.Debugf("Querying kforward proxy status endpoint: %s", statusURL)

	// Create an HTTP client with a timeout
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	// Make the GET request
	resp, err := client.Get(statusURL)
	if err != nil {
		logger.Errorw("Failed to connect to kforward proxy admin server", "url", statusURL, "error", err)
		if os.IsTimeout(err) {
			return fmt.Errorf("connection to admin server at %s timed out. Is 'kforward proxy' running with admin server enabled on port %d?", statusURL, statusAdminPort)
		}
		return fmt.Errorf("could not connect to admin server at %s (connection refused?). Ensure 'kforward proxy' is running and using admin port %d. Error: %w", statusURL, statusAdminPort, err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			logger.Errorw("Failed to close response body", "url", statusURL, "error", err)
		}
	}(resp.Body)

	// Check the HTTP status code
	if resp.StatusCode != http.StatusOK {
		logger.Errorw("Received non-OK status from admin server", "url", statusURL, "statusCode", resp.StatusCode)
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("kforward proxy admin server at %s returned status %d. Response: %s", statusURL, resp.StatusCode, string(bodyBytes))
	}

	// Read the response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorw("Failed to read response body from admin server", "url", statusURL, "error", err)
		return fmt.Errorf("failed to read response from %s: %w", statusURL, err)
	}

	// Parse the JSON response
	var statusEntries []manager.ForwardStatusEntry
	if err := json.Unmarshal(bodyBytes, &statusEntries); err != nil {
		logger.Errorw("Failed to parse JSON response from admin server", "url", statusURL, "responseBody", string(bodyBytes), "error", err)
		return fmt.Errorf("invalid JSON received from %s: %w", statusURL, err)
	}

	// Print the status in a formatted table
	if len(statusEntries) == 0 {
		fmt.Println("No active forwards reported by the kforward proxy.")
		return nil
	}

	fmt.Println("Active kforward Port Forwards:")
	err = printStatusTable(statusEntries)
	if err != nil {
		return err
	}

	return nil
}

// printStatusTable formats and prints the status entries using tabwriter.
func printStatusTable(entries []manager.ForwardStatusEntry) (err error) {
	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', tabwriter.TabIndent)
	defer func() {
		flushErr := writer.Flush()
		if err == nil && flushErr != nil {
			err = fmt.Errorf("failed to flush status table writer: %w", flushErr)
		}
	}()

	_, err = fmt.Fprintln(writer, "NAMESPACE\tSERVICE NAME\tSERVICE PORT\tLOCAL PORT")
	if err != nil {
		return fmt.Errorf("failed to write status table header: %w", err)
	}

	_, err = fmt.Fprintln(writer, "---------\t------------\t------------\t----------")
	if err != nil {
		return fmt.Errorf("failed to write status table separator: %w", err)
	}

	// Rows
	for _, entry := range entries {
		_, err = fmt.Fprintf(writer, "%s\t%s\t%d\t%d\n",
			entry.Namespace,
			entry.ServiceName,
			entry.ServicePort,
			entry.LocalPort,
		)
		if err != nil {
			return fmt.Errorf("failed to write status entry for %s/%s:%d: %w",
				entry.Namespace, entry.ServiceName, entry.ServicePort, err)
		}
	}

	return nil

}
