package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/sanspareilsmyn/kforward/internal/manager"

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
		Long: `Connects to a running 'kforward proxy' instance via its admin port (/status endpoint)
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

	// 1. Fetch raw status data from the admin server
	statusURL := fmt.Sprintf("http://127.0.0.1:%d/status", statusAdminPort)
	logger.Debugf("Querying kforward proxy status endpoint: %s", statusURL)

	bodyBytes, err := fetchStatusData(statusURL)
	if err != nil {
		logger.Errorw("Failed to fetch status data", "url", statusURL, "error", err)
		return err
	}
	logger.Debugw("Successfully fetched status data", "url", statusURL, "size", len(bodyBytes))

	// 2. Parse the fetched data
	statusEntries, err := parseStatusData(bodyBytes)
	if err != nil {
		logger.Errorw("Failed to parse status data", "url", statusURL, "rawData", string(bodyBytes), "error", err)
		return fmt.Errorf("invalid response received from admin server at %s: %w", statusURL, err)
	}
	logger.Debugw("Successfully parsed status data", "entryCount", len(statusEntries))

	// 3. Display the parsed data
	err = displayStatus(statusEntries)
	if err != nil {
		logger.Errorw("Failed to display status", "error", err)
		return err // Return the error directly
	}

	logger.Debug("Status command finished successfully.")
	return nil
}

// fetchStatusData handles making the HTTP GET request to the admin server's status endpoint.
// It returns the raw response body as a byte slice on success.
func fetchStatusData(url string) ([]byte, error) {
	logger := zap.S()

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		if os.IsTimeout(err) {
			return nil, fmt.Errorf("connection to admin server at %s timed out. Is 'kforward proxy' running with admin server enabled on port %d?", url, statusAdminPort)
		}
		return nil, fmt.Errorf("could not connect to admin server at %s (connection refused?). Ensure 'kforward proxy' is running and using admin port %d. Error: %w", url, statusAdminPort, err)
	}

	defer func() {
		closeErr := resp.Body.Close()
		if closeErr != nil {
			logger.Warnw("Failed to close response body", "url", url, "error", closeErr)
		}
	}()

	// Read the body first regardless of status code to potentially capture error details from the server
	bodyBytes, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		logger.Errorw("Failed to read response body from admin server", "url", url, "statusCode", resp.StatusCode, "error", readErr)
		return nil, fmt.Errorf("failed to read response from %s: %w", url, readErr)
	}

	// Now check the status code after successfully reading the body
	if resp.StatusCode != http.StatusOK {
		logger.Errorw("Received non-OK status from admin server", "url", url, "statusCode", resp.StatusCode, "responseBody", string(bodyBytes))
		return nil, fmt.Errorf("kforward proxy admin server at %s returned status %d. Response: %s", url, resp.StatusCode, string(bodyBytes))
	}

	// Status is OK, return the successfully read body bytes
	logger.Debugw("Received OK status from admin server", "url", url)
	return bodyBytes, nil
}

// parseStatusData parses the JSON byte slice into a slice of ForwardStatusEntry structs.
func parseStatusData(body []byte) ([]manager.ForwardStatusEntry, error) {
	var statusEntries []manager.ForwardStatusEntry

	if err := json.Unmarshal(body, &statusEntries); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	return statusEntries, nil
}

// displayStatus handles printing the status information to the console.
func displayStatus(entries []manager.ForwardStatusEntry) error {
	if len(entries) == 0 {
		fmt.Println("No active forwards reported by the kforward proxy.")
		return nil
	}

	fmt.Println("Active kforward Port Forwards:")
	err := printStatusTable(entries)
	if err != nil {
		return err
	}

	return nil
}

// printStatusTable formats and prints the status entries using a tabwriter for alignment.
// It returns an error if any write operation to os.Stdout fails.
func printStatusTable(entries []manager.ForwardStatusEntry) (err error) {
	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', tabwriter.TabIndent)

	defer func() {
		flushErr := writer.Flush()
		if err == nil && flushErr != nil {
			err = fmt.Errorf("failed to flush status table writer: %w", flushErr)
		}
	}()

	// Write table header, checking for errors after each write
	_, err = fmt.Fprintln(writer, "NAMESPACE\tSERVICE NAME\tSERVICE PORT\tLOCAL PORT")
	if err != nil {
		return fmt.Errorf("failed to write status table header: %w", err)
	}

	_, err = fmt.Fprintln(writer, "---------\t------------\t------------\t----------")
	if err != nil {
		return fmt.Errorf("failed to write status table separator: %w", err)
	}

	// Write table rows
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

	return err
}
