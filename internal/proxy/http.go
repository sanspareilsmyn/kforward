package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"github.com/sanspareilsmyn/kforward/internal/manager"
	"go.uber.org/zap"
)

const (
	// Timeouts for the underlying HTTP server
	httpServerReadTimeout  = 30 * time.Second
	httpServerWriteTimeout = 60 * time.Second
	httpServerIdleTimeout  = 120 * time.Second

	// Timeouts for the custom transport dialing kubectl
	transportDialTimeout           = 10 * time.Second
	transportKeepAlive             = 30 * time.Second
	transportTLSHandshakeTimeout   = 10 * time.Second
	transportExpectContinueTimeout = 1 * time.Second

	// connectDialTimeout is removed as CONNECT handler is removed
)

// Server wraps the HTTP proxy server and its dependencies.
type Server struct {
	reverseProxy *httputil.ReverseProxy
	mgr          *manager.Manager
	localPort    int
	httpServer   *http.Server
	logger       *zap.SugaredLogger
}

// NewHTTPServer creates a new HTTP proxy server instance (HTTPS CONNECT removed).
func NewHTTPServer(mgr *manager.Manager, localPort int) (*Server, error) {
	if mgr == nil {
		return nil, errors.New("manager cannot be nil")
	}
	if localPort <= 0 || localPort > 65535 {
		return nil, fmt.Errorf("invalid local port: %d", localPort)
	}

	logger := zap.S().With("component", "http-proxy", "listenPort", localPort)
	server := &Server{
		mgr:       mgr,
		localPort: localPort,
		logger:    logger,
	}

	// Setup Custom Transport for Reverse Proxy
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			connLogger := server.logger.With("targetAddr", addr)
			connLogger.Debug("Transport DialContext called")

			localFwdAddr, err := server.resolveLocalForwardAddress(addr)
			if err != nil {
				return nil, fmt.Errorf("cannot resolve local forward address for %s: %w", addr, err)
			}
			connLogger = connLogger.With("localFwdAddr", localFwdAddr)

			dialer := net.Dialer{
				Timeout:   transportDialTimeout,
				KeepAlive: transportKeepAlive,
			}
			localConn, err := dialer.DialContext(ctx, "tcp", localFwdAddr)
			if err != nil {
				connLogger.Errorw("Failed to dial local forward address", "error", err)
				return nil, fmt.Errorf("failed to connect to local forwarder port %s: %w", localFwdAddr, err)
			}
			connLogger.Debug("Successfully dialed local forwarder")
			return localConn, nil
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   transportTLSHandshakeTimeout,
		ExpectContinueTimeout: transportExpectContinueTimeout,
		MaxIdleConnsPerHost:   10,
	}

	// Setup Reverse Proxy
	reverseProxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = req.Host
			req.RequestURI = ""
			removeHopByHopHeaders(req.Header)
			if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
				req.Header.Set("X-Forwarded-For", clientIP)
			}
			req.Header.Set("X-Forwarded-Host", req.Host)
			req.Header.Set("X-Forwarded-Proto", "http")
			server.logger.Debugw("ReverseProxy Director rewriting request",
				"originalHost", req.Host, "method", req.Method, "targetURL", req.URL.String())
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			reqLogger := server.logger.With("targetHost", r.Host, "method", r.Method, "url", r.URL.String())
			reqLogger.Errorw("ReverseProxy error", "error", err)
			statusCode := http.StatusBadGateway
			statusMsg := "Bad Gateway"
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				statusCode = http.StatusGatewayTimeout
				statusMsg = "Gateway Timeout (Client Disconnected or Backend Timeout)"
			} else if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
				statusCode = http.StatusGatewayTimeout
				statusMsg = "Gateway Timeout (Backend Connection Timeout)"
			} else if errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "connection refused") {
				statusCode = http.StatusServiceUnavailable
				statusMsg = "Service Unavailable (Forwarder Disconnected)"
			} else if strings.Contains(err.Error(), "required port forward not active") {
				statusCode = http.StatusServiceUnavailable
				statusMsg = "Service Unavailable (Forwarder Not Found)"
			}
			http.Error(w, statusMsg, statusCode)
		},
		ModifyResponse: func(resp *http.Response) error {
			removeHopByHopHeaders(resp.Header)
			server.logger.Debugw("ReverseProxy ModifyResponse received",
				"statusCode", resp.StatusCode, "status", resp.Status, "requestHost", resp.Request.Host)
			return nil
		},
		FlushInterval: -1,
	}
	server.reverseProxy = reverseProxy

	// Setup Main Handler
	mainHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestLogger := server.logger.With(
			"method", r.Method, "uri", r.RequestURI, "proto", r.Proto,
			"host", r.Host, "remoteAddr", r.RemoteAddr,
		)
		requestLogger.Infow("Received request")

		if r.Method == http.MethodConnect {
			requestLogger.Warnw("CONNECT method is not supported by this proxy")
			http.Error(w, "CONNECT method not supported", http.StatusMethodNotAllowed)
			return
		}

		server.reverseProxy.ServeHTTP(w, r)
	})

	// Configure Underlying HTTP Server
	server.httpServer = &http.Server{
		Addr:         fmt.Sprintf("127.0.0.1:%d", localPort),
		Handler:      mainHandler,
		ReadTimeout:  httpServerReadTimeout,
		WriteTimeout: httpServerWriteTimeout,
		IdleTimeout:  httpServerIdleTimeout,
		ErrorLog:     zap.NewStdLog(server.logger.Desugar().Named("http-server-internal")),
	}

	logger.Info("HTTP Proxy server initialized (HTTPS CONNECT support disabled)")
	return server, nil
}

// resolveLocalForwardAddress parses host/port and uses the manager to find the local forward addr.
func (s *Server) resolveLocalForwardAddress(targetAddr string) (string, error) {
	logger := s.logger.With("operation", "resolve-local-fwd", "targetAddr", targetAddr)
	logger.Debug("Resolving local forward address")

	host, portStr, err := net.SplitHostPort(targetAddr)
	if err != nil {
		logger.Warnw("Invalid target address format", "error", err)
		return "", fmt.Errorf("invalid target address format '%s': %w", targetAddr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		logger.Warnw("Invalid target port", "portStr", portStr, "error", err)
		return "", fmt.Errorf("invalid target port '%s': %w", portStr, err)
	}
	logger = logger.With("targetHost", host, "targetPort", port)

	var namespace, serviceName string
	parts := strings.SplitN(host, ".", 3)
	if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
		serviceName = parts[0]
		namespace = parts[1]
	}
	if namespace == "" || serviceName == "" {
		logger.Warnw("Could not parse namespace/service from host", "host", host)
		return "", fmt.Errorf("invalid Kubernetes service host format: %s (expecting service.namespace...)", host)
	}
	logger = logger.With("namespace", namespace, "serviceName", serviceName)

	logger.Debug("Querying manager for local address")
	localFwdAddr, err := s.mgr.GetLocalAddress(namespace, serviceName, port)
	if err != nil {
		logger.Warnw("Manager could not find active local forward address", "error", err)
		return "", fmt.Errorf("required port forward not active for %s/%s:%d: %w", namespace, serviceName, port, err)
	}

	logger.Infow("Resolved local forward address successfully", "localFwdAddr", localFwdAddr)
	return localFwdAddr, nil
}

// ListenAndServe starts the underlying HTTP server.
func (s *Server) ListenAndServe() error {
	s.logger.Infow("HTTP Proxy server starting", "address", s.httpServer.Addr)
	err := s.httpServer.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.logger.Errorw("HTTP Proxy server ListenAndServe failed", "error", err)
		return fmt.Errorf("http proxy server failed: %w", err)
	}
	s.logger.Info("HTTP Proxy server stopped gracefully")
	return nil
}

// Shutdown gracefully stops the underlying HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("Attempting graceful shutdown of HTTP proxy server...")
	err := s.httpServer.Shutdown(ctx)
	if err != nil {
		s.logger.Warnw("HTTP proxy server shutdown failed", "error", err)
		return err
	}
	s.logger.Info("HTTP proxy server shutdown completed.")
	return nil
}

// removeHopByHopHeaders removes connection-specific headers.
var hopByHopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func removeHopByHopHeaders(header http.Header) {
	if c := header.Get("Connection"); c != "" {
		for _, f := range strings.Split(c, ",") {
			header.Del(strings.TrimSpace(f))
		}
	}
	for _, h := range hopByHopHeaders {
		header.Del(h)
	}
}
