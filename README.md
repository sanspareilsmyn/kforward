<p>
  <img src="assets/logo.png" alt="kforward logo" width="200"/>
  <!-- width="200" 값은 로고 크기에 맞게 조절하세요 -->
</p>

# kforward

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8.svg)](https://golang.org/) <!-- Update Go Version if needed -->
[![Build Status](https://img.shields.io/badge/Build-Passing-brightgreen.svg)]() <!-- TODO: Replace with actual CI badge -->
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

**kforward** is a lightweight, simple CLI tool designed to streamline local development for applications interacting with Kubernetes services. Built with Go, it allows you to easily access services running inside your cluster using their standard service names, without the hassle of managing multiple `kubectl port-forward` sessions.

## 🤔 Why kforward?_

Developing applications locally that need to communicate with services inside a Kubernetes cluster often involves friction:

*   **Tedious Port-Forwarding:** Managing multiple `kubectl port-forward` commands in separate terminals for each required service is cumbersome, error-prone, and doesn't scale well.
*   **Configuration Changes:** Hardcoding `localhost:PORT` addresses in local configurations is inconvenient and differs from how the application runs in the cluster.
*   **Complex Alternatives:** Tools like Telepresence or mirrord are powerful but can be complex to set up, require specific cluster configurations, or might be overkill for simply accessing internal services.

**kforward aims to address these challenges by providing a simpler approach:**

*   **🚀 Simple & Reliable:** Provides a straightforward local HTTP/HTTPS proxy interface while using the battle-tested `kubectl port-forward` for the actual tunneling.
*   **kubectl Automation:** Automatically finds target pods and manages the lifecycle of required `kubectl port-forward` processes for specified services or entire namespaces.
*   **📦 Single Binary:** The `kforward` tool itself is a single static Go binary, easy to install and run. **(Note: Requires `kubectl` to be installed and available in your PATH).**
*   **🌐 Use Native Service Names:** Allows your local application to use standard Kubernetes service DNS names (e.g., `my-service.my-namespace.svc.cluster.local`) without code changes, just by configuring the proxy.
*   **✅ Improved Developer Experience:** Reduces the friction of managing port forwards manually, making local development against Kubernetes smoother and less error-prone.

By automating `kubectl port-forward` management behind a simple proxy, kforward helps developers **focus on coding**, **reduce configuration errors**, and **maintain consistency** between local and cluster environments.

## ✨ Features

*   **CLI Interface:** Simple command-line interface to start the proxy and specify targets.
*   **Kubeconfig Autodetection:** Automatically uses your currently configured `kubectl` context.
*   **Context Override:** Supports specifying a Kubernetes context via the `--context` flag.
*   **kubectl Process Management:**
    *   Automatically finds ready pods for specified services.
    *   Starts `kubectl port-forward` processes in the background for required services/ports.
    *   Manages and terminates these background `kubectl` processes gracefully when `kforward` exits.
*   **Targeted Service Forwarding:** Manages forwards for one or more specific services (`namespace/service-name`).
*   **Namespace-Wide Forwarding:** Manages forwards for all non-headless services and their ports within a specified namespace.
*   **Local HTTP/HTTPS Proxy:** Runs a local proxy server (default port `1080`) that handles:
    *   Standard HTTP requests.
    *   HTTPS requests via the `CONNECT` method (TCP tunneling).
*   **Dynamic Connection Routing:** Routes incoming proxy requests to the correct background `kubectl port-forward` process based on the requested service name and port.
*   **Configurable Proxy Port:** Allows specifying a different local port for the HTTP/HTTPS proxy server using the `--port` flag.

## 🏗️ Architecture

kforward runs as a **local Go process** that acts as a central manager and proxy. It uses `client-go` to query the Kubernetes API and `os/exec` to launch and manage `kubectl port-forward` processes. Your local application connects to the kforward proxy, which then routes traffic through the appropriate `kubectl` tunnel.
```text
+------------------------------------------+      +--------------------------------+      +-------------------------+
|            Your Local Machine            |      |      Kubernetes Cluster        |      |   Target Pod (in K8s)   |
|                                          |      |                                |      |                         |
|  +----------------+                      |      |  +-------------------------+   |      |  +-------------------+  |
|  | Your Local App |--------------------->|----->|  | kforward HTTP Proxy     |   |      |  | Your App's Backend|  |
|  | (e.g., my-app) |                      |      |  | (Go Process)            |   |      |  | (e.g., Port 8080) |  |
|  | Configured with|                      |      |  | Listens on :1080        |   |      |  +---------^---------+  |
|  | proxy settings |                      |      |  +-----------+-------------+   |      |           | kubectl tunnel|
|  +----------------+                      |      |              |                 |      |           | established |
|                                          |      |              | Uses            |      +-----------+-----------+
|                                          |      |              | manager to find |                  ^
|                                          |      |              | local port map  |                  |
|                                          |      |              v                 |                  |
|                                          |      |  +-----------+-------------+   |                  |
|  +------------------+<-------------------|------|  | kubectl port-fwd Proc 1 |<--+ K8s API Server   |
|  | localhost:10001  | Dials local port   |      |  | (Background Process)    |---| (Port Forward)   |
|  +------------------+                    |      |  +-------------------------+   |                  |
|                                          |      |                                |                  |
|  +------------------+                    |      |  +-------------------------+   |                  |
|  | localhost:10002  |<-------------------|------|  | kubectl port-fwd Proc 2 |<--+  K8s API Server  |
|  +------------------+                    |      |  | (Background Process)    |---| (Port Forward)   |
|        ^                                 |      |  +-------------------------+   |
|        | kforward Manager starts         |      +--------------------------------+
|        | & manages these kubectl processes |
|        | based on user flags.              |
+------------------------------------------+
```
**Key Components & Flow:**

1.  **kforward Application (Local Host):**
    *   Runs directly on your machine (`./kforward`).
    *   Starts a local **HTTP/HTTPS proxy** server (e.g., on `localhost:1080`).
    *   Uses your `kubeconfig` and `client-go` to connect to the **Kubernetes API Server** to get service/pod information.
    *   Based on the `--namespace` or `--service` flags, the **Manager** component identifies necessary port forwards.
    *   For each required forward, the Manager **launches a `kubectl port-forward pod/... <local-port>:<pod-port>` command** as a background process. It keeps track of which service/port maps to which local port (e.g., `10001`, `10002`).
2.  **Local Development Application (`my-app`):**
    *   Your application that needs to talk to K8s services.
    *   Configured to use the kforward HTTP/HTTPS proxy (e.g., via `export http_proxy=http://localhost:1080 https_proxy=http://localhost:1080`).
    *   Makes requests using the **standard Kubernetes service DNS name** (e.g., `http://hello-app-service.default.svc.cluster.local`).
3.  **Request Handling:**
    *   The request from `my-app` is directed to the local `kforward` proxy (`localhost:1080`).
    *   The **HTTP Proxy** component receives the request.
        *   It identifies the target Kubernetes service (e.g., `hello-app-service.default`) and port (e.g., 80).
        *   It asks the **Manager** for the local port that corresponds to this target (e.g., `10001`).
        *   The proxy then **dials the local port** (`localhost:10001`).
    *   The background **`kubectl port-forward` process** listening on `localhost:10001` receives this connection.
    *   `kubectl` handles the secure tunneling through the **K8s API Server** to the actual Pod (e.g., `hello-app-deployment-xxxx:8080`).
    *   The response travels back through the `kubectl` tunnel, to the `kforward` proxy, and finally to your `my-app`.

---
## 🚀 Getting Started

This guide helps you run `kforward` locally to access services in your existing Kubernetes cluster.

1.  **Prerequisites:**
    *   Go (Version 1.22+ recommended - check `go.mod`)
    *   Git
    *   **`kubectl`**: Must be installed and available in your system's PATH. kforward uses it under the hood. Verify with `kubectl version --client`.
    *   A Kubernetes cluster (e.g., Minikube, Kind, Docker Desktop K8s, or a remote cluster) accessible via your `kubectl` configuration.

2.  **Fork & Clone:**
    *   Fork the repository on GitHub.
    *   Clone your fork locally:
        ```bash
        git clone https://github.com/<your-username>/kforward.git # Replace with your repo path
        cd kforward
        ```

3.  **Deploy a Test Service (if needed):**
    *   To test `kforward`, you need a service running in your cluster. Use the example `hello-app.yaml` or any other service.
        ```bash
        # Example: Deploy hello-app from the examples directory
        kubectl apply -f examples/hello-app.yaml [-n your-namespace]
        ```
    *   Verify the deployment and service are running:
        ```bash
        kubectl get deployment hello-app-deployment [-n your-namespace]
        kubectl get service hello-app-service [-n your-namespace]
        ```

4.  **Build kforward:**
    *   Fetch dependencies:
        ```bash
        go mod tidy
        ```
    *   Build the binary:
        ```bash
        go build -o kforward ./cmd/kforward
        ```

5.  **Run kforward:**
    *   Start the proxy, specifying the target namespace or service(s). Keep this terminal running.
    *   **Use `--context` if your desired cluster isn't the default.** Find contexts with `kubectl config get-contexts`.
        ```bash
        # Example 1: Manage forwards for all services in 'default' namespace using 'docker-desktop' context
        ./kforward proxy --context docker-desktop --namespace default --port 1080
        ```
        ```bash
        # Example 2: Manage forward for only 'hello-app-service' in 'default' namespace
        # Note: Service port is auto-detected. If multiple ports exist, the first is chosen by default.
        ./kforward proxy --context docker-desktop --service default/hello-app-service --port 1080
        ```
    *   Watch the logs. You should see:
        *   Confirmation of the Kubernetes context being used.
        *   Messages indicating `kubectl port-forward` processes are being started (e.g., `Starting kubectl process: ...`).
        *   The HTTP Proxy server listening message.

6.  **Configure Your Client Environment:**
    *   In **a separate terminal**, set the `http_proxy` and `https_proxy` environment variables (lowercase recommended).
        ```bash
        # If kforward is running on port 1080
        export http_proxy="http://localhost:1080"
        export https_proxy="http://localhost:1080"

        # Optional but recommended: Configure no_proxy for hosts that shouldn't use the proxy
        export no_proxy="localhost,127.0.0.1,.local,.your-internal-domain.com"
        ```
        *   **Alternatively (Command-Specific):** Prefix your command directly:
            ```bash
            http_proxy="http://localhost:1080" https_proxy="http://localhost:1080" curl ...
            ```
        *   **Alternatively (curl direct flag):** Use `curl -x http://localhost:1080 ...`

7.  **Test the Connection:**
    *   From the **terminal where you set the proxy environment variables**, use `curl` (or your browser/application) to access the service using its Kubernetes DNS name.
        ```bash
        # Test hello-app-service (HTTP on service port 80)
        curl http://hello-app-service.default.svc.cluster.local

        # Test another service (replace names/ports accordingly)
        # curl http://my-api.my-namespace.svc.cluster.local:8080
        # curl -k https://secure-service.other-ns.svc.cluster.local # -k if self-signed cert
        ```
    *   Check the `kforward` logs (first terminal) for activity: `Received request`, `Rewriting request`, `DialContext called`, `Found local forward address`, `Successfully dialed`.
    *   You should receive the correct response in your `curl` terminal.

8.  **Stopping kforward:**
    *   Go back to the first terminal (where `kforward` is running) and press `Ctrl+C`.
    *   `kforward` should log that it's stopping the proxy server and terminating the background `kubectl` processes.
    *   In the second terminal, unset the proxy environment variables if you used `export`: `unset http_proxy https_proxy no_proxy`.

---

## 🙌 Contributing

We welcome contributions! Please see `CONTRIBUTING.md` for details on how to contribute.

---

## 📄 License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the full license text.
