# Contributing to kforward

First off, thank you for considering contributing to kforward! 🎉 We welcome any help to make this project better. Whether it's reporting a bug, proposing a new feature, improving documentation, or writing code, your contribution is valuable.

This document provides guidelines for contributing to kforward. Please take a moment to review it.

## Code of Conduct

While we don't have a formal Code of Conduct yet, we expect all contributors to interact respectfully and constructively. Please be kind and considerate in all discussions and contributions. Harassment or exclusionary behavior will not be tolerated.

## How Can I Contribute?

There are many ways to contribute:

*   **🐛 Reporting Bugs:** If you find a bug, please open an issue on GitHub. Provide as much detail as possible, including steps to reproduce, `kforward` logs, expected behavior, and actual behavior.
*   **✨ Suggesting Enhancements:** Open an issue to discuss new features or improvements. Explain the use case and the proposed functionality.
*   **📝 Improving Documentation:** Pull Requests (PRs) for documentation improvements (README, code comments, etc.) are always welcome!
*   **💻 Writing Code:**
    1.  **Discuss First (for significant changes):** It's often best to open an issue to discuss major changes before starting work, to ensure alignment.
    2.  **Follow the Workflow:** See the "Contribution Workflow" section below.

## Getting Started (Development Setup)

To contribute code, you'll need a local development environment. `kforward` runs locally, uses `kubectl` in the background, and interacts with an existing Kubernetes cluster.

1.  **Prerequisites:**
    *   Go (Version 1.22+ recommended - check `go.mod`)
    *   Git
    *   **`kubectl`**: Must be installed and available in your system's PATH. Verify with `kubectl version --client`.
    *   A Kubernetes cluster (e.g., Minikube, Kind, Docker Desktop K8s, or a remote cluster) accessible via your `kubectl` configuration. This is where you'll run test services.
    *   (Recommended) `pre-commit` tool (install via `pip install pre-commit` or `brew install pre-commit`) if you intend to commit code.

2.  **Fork & Clone:**
    *   Fork the repository on GitHub.
    *   Clone your fork locally:
        ```bash
        git clone https://github.com/<your-username>/kforward.git # Replace with your repo path
        cd kforward
        ```

3.  **Deploy a Test Service in Kubernetes (Crucial!):**
    *   `kforward` needs a target service inside your Kubernetes cluster for testing.
    *   Deploy the simple `hello-app` example (e.g., from `examples/hello-app.yaml`) or use your own test service.
    *   Ensure the Deployment and a `ClusterIP` Service are running:
        ```bash
        # Example using hello-app.yaml
        kubectl apply -f examples/hello-app.yaml [-n your-namespace]

        # Verify pods and service are running
        kubectl get pods -l app=hello-app [-n your-namespace]
        kubectl get service hello-app-service [-n your-namespace]
        ```
    *   Make note of the service name and namespace.

4.  **Set up Pre-commit Hooks (Recommended):**
    *   If installed, run `pre-commit install` in the repository root.

5.  **Dependencies & Local Build:**
    *   Fetch Go dependencies:
        ```bash
        go mod tidy
        ```
    *   Build the `kforward` binary **locally**:
        ```bash
        go build -o kforward ./cmd/kforward
        ```

6.  **Running & Testing the System:**
    *   **1. Ensure Kubernetes Cluster is Accessible:** Verify `kubectl cluster-info` works and your test service is running.
    *   **2. Run kforward Locally:** In **Terminal 1**, start the `kforward` binary you built. Keep this terminal running.
        *   Specify the target namespace (`--namespace`) or service (`--service`).
        *   Use the **`--context`** flag to specify the correct Kubernetes context for your test cluster (find names with `kubectl config get-contexts`).
        ```bash
        # Example: Manage forwards for 'default' namespace using 'docker-desktop' context
        ./kforward proxy --context docker-desktop --namespace default --port 1080
        ```
        *   Observe the logs for successful initialization and messages indicating `kubectl port-forward` processes are starting.
    *   **3. Configure Client Environment for Testing:** In **Terminal 2**, set the **HTTP/HTTPS proxy environment variables** (lowercase recommended):
        ```bash
        export http_proxy="http://localhost:1080"  # Use the port kforward is listening on
        export https_proxy="http://localhost:1080" # Use the same for HTTPS via CONNECT
        export no_proxy="localhost,127.0.0.1"     # Prevent proxying local requests
        ```
    *   **4. Test Connection via kforward:** From **Terminal 2** (where proxy env vars are set), use `curl` to access your test service using its Kubernetes DNS name. **Do NOT use extra flags like `--socks5-hostname`.**
        ```bash
        # Test HTTP service
        curl http://hello-app-service.default.svc.cluster.local

        # Test HTTPS service (add -k if using self-signed certs)
        # curl [-k] https://my-secure-service.my-ns.svc.cluster.local
        ```
        *   **Expected Result:** You should get the correct response from the service in Terminal 2.
        *   **Check Logs:** Observe the logs in Terminal 1 (`kforward`) to see the proxy request handling, manager lookups, and potentially output from the background `kubectl` processes.
    *   **5. Run Unit Tests:** Run Go unit tests (these typically don't require a running cluster).
        ```bash
        go test ./...
        ```

7.  **Stopping the Environment:**
    *   Stop the `kforward` application in **Terminal 1** (usually `Ctrl+C`). It should attempt to terminate the background `kubectl` processes.
    *   Unset the environment variables in **Terminal 2**: `unset http_proxy https_proxy no_proxy`.
    *   Optionally, delete the test service from Kubernetes: `kubectl delete -f examples/hello-app.yaml [-n your-namespace]`.

## Contribution Workflow

1.  **Fork & Branch:** Fork the repo and create a descriptive branch from `main`: `git checkout -b feat/describe-your-feature` or `fix/fix-that-bug`.
2.  **Develop:** Make your code changes. Add or update unit/integration tests. Update documentation.
3.  **Test:** Run `go test ./...`. Perform manual testing using the steps in "Running & Testing the System".
4.  **Pre-commit:** Ensure pre-commit checks pass (`pre-commit run --all-files` if needed).
5.  **Commit:** Use Conventional Commits format (e.g., `git commit -m "feat: implement on-demand kubectl start"`).
6.  **Push:** Push your branch to your fork: `git push origin feat/your-branch-name`.
7.  **Open a Pull Request (PR):** Create a PR to the main repository's `main` branch. Provide a clear title/description, link issues, explain changes.

## Pull Request Process

1.  **Review:** Maintainer review and feedback.
2.  **CI Checks:** Automated checks must pass.
3.  **Discussion & Iteration:** Address feedback and update your branch.
4.  **Approval & Merge:** Merge into `main`.

## Questions?

Feel free to open an issue on GitHub.

Thank you for contributing to kforward! 🙏
