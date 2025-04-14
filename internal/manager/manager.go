package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sanspareilsmyn/kforward/internal/k8s"
	"go.uber.org/zap"
)

const (
	defaultStartLocalPort  = 10000                  // Start allocating local ports from here
	maxPortCheckAttempts   = 100                    // Max attempts to find an available local port
	processStopTimeout     = 10 * time.Second       // Timeout waiting for process exit confirmation
	processKillGracePeriod = 200 * time.Millisecond // Time between Cancel/SIGTERM and SIGKILL
	initialForwardWait     = 500 * time.Millisecond // Quick delay hoping kubectl starts fast
)

// ForwardedPortInfo holds details about an active port forward managed by kubectl.
type ForwardedPortInfo struct {
	TargetKey string             // Unique key (e.g., "namespace/service:port")
	LocalPort int                // The local port kubectl is listening on
	PodName   string             // The specific pod being forwarded to
	PodPort   int                // The target port on the pod
	Namespace string             // The namespace of the pod/service
	Cmd       *exec.Cmd          // Handle to the running kubectl process
	stopFunc  func()             // Function to stop the process group
	errChan   chan error         // Channel receives error when process exits (nil for clean exit)
	cancelCtx context.CancelFunc // Context cancellation function specifically for this process
}

// Manager handles the lifecycle of kubectl port-forward processes.
type Manager struct {
	k8sClient   *k8s.Client
	kubeContext string
	logger      *zap.SugaredLogger

	// Map key: "namespace/serviceName:servicePort", value: *ForwardedPortInfo
	activeForwards map[string]*ForwardedPortInfo
	mapMutex       sync.RWMutex // Mutex to protect the activeForwards map

	// For allocating local ports
	nextLocalPort int
	portMutex     sync.Mutex
}

// NewManager creates a new port forward manager.
func NewManager(k8sClient *k8s.Client, kubeContext string) *Manager {
	return &Manager{
		k8sClient:      k8sClient,
		kubeContext:    kubeContext,
		logger:         zap.S().With("component", "portforward-manager"),
		activeForwards: make(map[string]*ForwardedPortInfo),
		nextLocalPort:  defaultStartLocalPort,
	}
}

// StartForwardingForService sets up and starts a 'kubectl port-forward' process
// for a specific service and port if it's not already running.
func (m *Manager) StartForwardingForService(ctx context.Context, namespace, serviceName string, servicePort int) error {
	mapKey := generateMapKey(namespace, serviceName, servicePort)
	logger := m.logger.With("target", mapKey)

	// Check if already active (Read Lock)
	m.mapMutex.RLock()
	existingInfo, exists := m.activeForwards[mapKey]
	m.mapMutex.RUnlock()
	if exists {
		// For now, assume if it's in the map, it's considered active.
		logger.Debugw("Port forward already active, skipping.", "localPort", existingInfo.LocalPort)
		return nil
	}

	logger.Infow("Initiating new port forward setup")

	// Allocate local port
	localPort := m.findAvailableLocalPort()
	if localPort == 0 {
		return fmt.Errorf("could not allocate a local port for %s", mapKey)
	}
	logger = logger.With("localPort", localPort)

	procCtx, procCancel := context.WithCancel(ctx)
	info := &ForwardedPortInfo{
		TargetKey: mapKey,
		LocalPort: localPort,
		Namespace: namespace,
		stopFunc:  nil,                 // Will be set after cmd is created
		errChan:   make(chan error, 1), // Buffered channel
		cancelCtx: procCancel,          // Store cancel function
	}

	m.mapMutex.Lock()
	// Double-check existence after acquiring write lock, in case of race
	if _, existsAgain := m.activeForwards[mapKey]; existsAgain {
		m.mapMutex.Unlock()
		procCancel()
		logger.Warnw("Port forward was started concurrently, skipping duplicate setup.")
		return nil
	}
	m.activeForwards[mapKey] = info
	m.mapMutex.Unlock()

	// Find Pod (Potentially long operation, outside lock)
	// Use the standalone helper function from the k8s package
	podName, podPort, err := k8s.FindReadyPodForServicePort(procCtx, m.k8sClient, namespace, serviceName, servicePort)
	if err != nil {
		logger.Errorw("Cannot find ready pod", "error", err)
		m.cleanupFailedForwardAttempt(mapKey, procCancel)
		return fmt.Errorf("cannot find ready pod for %s: %w", mapKey, err)
	}

	logger = logger.With("podName", podName, "podPort", podPort)
	logger.Infow("Found ready pod for forwarding")

	// Prepare and Start Kubectl Command
	cmd, err := m.prepareKubectlCmd(procCtx, namespace, podName, localPort, podPort)
	if err != nil {
		logger.Errorw("Failed to prepare kubectl command", "error", err)
		m.cleanupFailedForwardAttempt(mapKey, procCancel)
		return fmt.Errorf("failed to prepare kubectl command for %s: %w", mapKey, err)
	}

	logger.Infow("Starting kubectl process", "command", fmt.Sprintf("kubectl %s", strings.Join(cmd.Args[1:], " ")))
	err = cmd.Start()
	if err != nil {
		logger.Errorw("Failed to start kubectl process", "error", err)
		m.cleanupFailedForwardAttempt(mapKey, procCancel)
		return fmt.Errorf("failed to start kubectl for %s: %w", mapKey, err)
	}
	pid := cmd.Process.Pid
	logger = logger.With("pid", pid)
	logger.Infow("kubectl process started successfully")

	// Update map entry with final details (Write Lock)
	m.mapMutex.Lock()
	info, stillExists := m.activeForwards[mapKey]
	if !stillExists {
		m.mapMutex.Unlock()
		logger.Warnw("Map entry disappeared before final update, stopping potentially orphaned process", "pid", pid)
		// Attempt to stop the process we just started, as its map entry is gone
		m.terminateProcess(cmd, procCancel, logger)
		return fmt.Errorf("internal error: port forward info disappeared for %s", mapKey)
	}

	info.Cmd = cmd
	info.PodName = podName
	info.PodPort = podPort
	info.stopFunc = func() {
		m.terminateProcess(cmd, procCancel, logger)
	}
	m.mapMutex.Unlock()

	go m.waitForProcessExit(mapKey, info)

	time.Sleep(initialForwardWait)
	logger.Infow("Port forward initiated", "target", fmt.Sprintf("pod/%s:%d", podName, podPort), "local", fmt.Sprintf("localhost:%d", localPort))

	return nil
}

// generateMapKey creates a consistent key for the activeForwards map.
func generateMapKey(namespace, serviceName string, servicePort int) string {
	return fmt.Sprintf("%s/%s:%d", namespace, serviceName, servicePort)
}

// findAvailableLocalPort attempts to find an unused local TCP port.
func (m *Manager) findAvailableLocalPort() int {
	m.portMutex.Lock()
	defer m.portMutex.Unlock()

	logger := m.logger.With("operation", "find-local-port")
	logger.Debug("Attempting to find available local port")

	for i := 0; i < maxPortCheckAttempts; i++ {
		port := m.nextLocalPort
		// Ensure the port is above the reserved range (0-1024)
		if port <= 1024 {
			port = defaultStartLocalPort
		}
		m.nextLocalPort++

		// Wrap around if we reach the max port
		if m.nextLocalPort > 65535 {
			m.nextLocalPort = defaultStartLocalPort
		}

		// Quick check if the port is likely free by trying to listen
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		listener, err := net.Listen("tcp", addr)
		if err == nil {
			// Port seems free, close the listener immediately
			_ = listener.Close()
			logger.Debugw("Found available local port", "port", port)
			return port
		}
	}

	logger.Errorw("Could not find an available local port", "attempts", maxPortCheckAttempts)
	return 0 // Indicate failure
}

// prepareKubectlCmd creates the exec.Cmd for port-forwarding.
func (m *Manager) prepareKubectlCmd(ctx context.Context, namespace, podName string, localPort, podPort int) (*exec.Cmd, error) {
	args := []string{
		"port-forward",
		"--namespace", namespace,
		fmt.Sprintf("pod/%s", podName),
		fmt.Sprintf("%d:%d", localPort, podPort),
	}

	// Add context flag if specified for the manager
	if m.kubeContext != "" {
		args = append(args, "--context", m.kubeContext)
	}

	// Create the command with the specific process context
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	return cmd, nil
}

// terminateProcess handles stopping a kubectl process (Simplified approach).
func (m *Manager) terminateProcess(cmd *exec.Cmd, cancel context.CancelFunc, logger *zap.SugaredLogger) {
	if cmd == nil || cmd.Process == nil {
		logger.Warn("Terminate process called with nil command or process")
		cancel()
		return
	}

	pid := cmd.Process.Pid
	logger = logger.With("pid", pid)

	pgid, pgidErr := syscall.Getpgid(pid)
	canUsePgid := pgidErr == nil

	logger.Infow("Attempting to stop kubectl process", "pgid", pgid, "canUsePgid", canUsePgid)

	// 1. Cancel context first
	logger.Debug("Cancelling process context")
	cancel()

	// 2. Allow a short grace period for context cancellation to potentially take effect
	time.Sleep(processKillGracePeriod)

	// 3. Attempt SIGTERM (Graceful shutdown signal)
	logger.Debug("Sending SIGTERM...")
	var termErr error
	if canUsePgid {
		termErr = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		logger.Debugw("Cannot use PGID, sending SIGTERM to PID", "getPgidError", pgidErr)
		termErr = cmd.Process.Signal(syscall.SIGTERM)
	}

	if termErr != nil && !errors.Is(termErr, os.ErrProcessDone) && !errors.Is(termErr, syscall.ESRCH) { // ESRCH: No such process
		logger.Warnw("Sending SIGTERM failed (process might be unresponsive or already gone)", "error", termErr)
	} else {
		logger.Debugw("SIGTERM not needed or failed because process/group already gone", "error", termErr)
		return
	}

	// 4. Attempt SIGKILL (Forceful termination) - Assume SIGTERM might not have worked
	logger.Debug("Sending SIGKILL...")
	var killErr error
	if canUsePgid {
		killErr = syscall.Kill(-pgid, syscall.SIGKILL) // Kill the process group
	} else {
		logger.Debugw("Cannot use PGID, sending SIGKILL to PID", "getPgidError", pgidErr)
		killErr = cmd.Process.Kill()
	}

	// Log final outcome
	if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) && !errors.Is(killErr, syscall.ESRCH) {
		logger.Errorw("Sending SIGKILL failed", "error", killErr)
	} else {
		logger.Infow("Process likely terminated gracefully before SIGKILL or was already gone", "kill_error_ignored", killErr)
	}
}

// waitForProcessExit waits for a specific kubectl process to finish and cleans up.
func (m *Manager) waitForProcessExit(mapKey string, pInfo *ForwardedPortInfo) {
	logger := m.logger.With("target", mapKey)
	if pInfo.Cmd != nil && pInfo.Cmd.Process != nil {
		logger = logger.With("pid", pInfo.Cmd.Process.Pid)
	} else {
		logger.Warn("Wait goroutine started but command/process info is missing")
		pInfo.errChan <- errors.New("process info missing before wait")
		m.cleanupMapEntry(mapKey, pInfo.cancelCtx, logger)
		return
	}

	waitErr := pInfo.Cmd.Wait()
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			logger.Warnw("kubectl process exited with error", "exitCode", exitErr.ExitCode(), "error", waitErr)
		} else {
			logger.Warnw("kubectl process exited with unexpected error", "error", waitErr)
		}
	} else {
		logger.Info("kubectl process exited cleanly")
	}

	// Signal completion/error via the channel
	select {
	case pInfo.errChan <- waitErr:
		logger.Debug("Notified error channel of process exit status")
	case <-time.After(1 * time.Second): // Short timeout, channel should be buffered
		logger.Warn("Timeout sending exit status to error channel (potential listener issue?)")
	}

	m.cleanupMapEntry(mapKey, pInfo.cancelCtx, logger)
}

// cleanupMapEntry removes the forward info from the map and cancels its context.
func (m *Manager) cleanupMapEntry(mapKey string, cancel context.CancelFunc, logger *zap.SugaredLogger) {
	m.mapMutex.Lock()
	delete(m.activeForwards, mapKey)
	m.mapMutex.Unlock()

	logger.Info("Cleaned up map entry")
	cancel()
}

// cleanupFailedForwardAttempt removes the placeholder map entry for a forward that failed during setup.
func (m *Manager) cleanupFailedForwardAttempt(mapKey string, cancel context.CancelFunc) {
	m.logger.Debugw("Cleaning up map entry for failed forward setup", "target", mapKey)
	m.mapMutex.Lock()
	delete(m.activeForwards, mapKey)
	m.mapMutex.Unlock()
	cancel()
}

// GetLocalAddress looks up the local port mapped to a service endpoint.
func (m *Manager) GetLocalAddress(namespace, serviceName string, servicePort int) (string, error) {
	mapKey := generateMapKey(namespace, serviceName, servicePort)
	m.mapMutex.RLock()
	defer m.mapMutex.RUnlock()

	info, exists := m.activeForwards[mapKey]
	if !exists {
		m.logger.Debugw("No active port forward found in map for key", "target", mapKey)
		return "", fmt.Errorf("port forward for %s not active or not yet started", mapKey)
	}

	address := fmt.Sprintf("127.0.0.1:%d", info.LocalPort)
	return address, nil
}

// StopAll terminates all managed kubectl port-forward processes gracefully.
func (m *Manager) StopAll() {
	m.logger.Info("Stopping all active kubectl port-forward processes...")

	// Collect all active forwards to stop
	m.mapMutex.RLock()
	infosToStop := make([]*ForwardedPortInfo, 0, len(m.activeForwards))
	for _, info := range m.activeForwards {
		infosToStop = append(infosToStop, info)
	}
	m.mapMutex.RUnlock()

	if len(infosToStop) == 0 {
		m.logger.Info("No active forwards to stop.")
		return
	}
	m.logger.Infow("Found active forwards to stop.", "count", len(infosToStop))

	// Initiate stop for each process concurrently
	var wg sync.WaitGroup
	wg.Add(len(infosToStop))

	for _, pInfo := range infosToStop {
		go func(pi *ForwardedPortInfo) {
			defer wg.Done()
			logger := m.logger.With("target", pi.TargetKey)
			logger.Info("Initiating stop...")
			if pi.stopFunc != nil {
				pi.stopFunc()
			} else {
				logger.Warn("Stop function was nil, cancelling context directly")
				pi.cancelCtx()
			}

			// Wait for the process exit confirmation from the wait goroutine
			select {
			case exitErr, ok := <-pi.errChan:
				if ok {
					logger.Infow("Stop confirmed by wait goroutine.", "exitError", exitErr)
				} else {
					logger.Warn("Wait goroutine channel closed unexpectedly during stop.")
				}
			case <-time.After(processStopTimeout):
				logger.Errorw("Timeout waiting for kubectl process to confirm stop after signaling.", "timeout", processStopTimeout)
			}
		}(pInfo)
	}

	wg.Wait()
	m.logger.Info("Finished signaling stop for all processes.")
}
