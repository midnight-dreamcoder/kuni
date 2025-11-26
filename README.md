# K8s Universal Inspector

![License](https://img.shields.io/badge/license-GPLv3-blue.svg)
![Go Version](https://img.shields.io/badge/go-1.20%2B-cyan)
![Status](https://img.shields.io/badge/status-active-green)

**K8s Universal Inspector** is a high-performance, read-only multi-cluster dashboard designed for SREs and Developers who need to visualize, audit, and troubleshoot Kubernetes resources across dozens of clusters simultaneously.

Unlike standard tools that focus on a single cluster context, this tool aggregates data from multiple `kubeconfig` files, providing a unified view of your entire infrastructure.

## ✨ Key Features

### 🖥️ Multi-Cluster Management
* **Unified View:** View and filter resources across multiple clusters at once.
* **Auto-Discovery:** Automatically detects `config-ops*` kubeconfig files in your home directory.
* **AWS EKS Integration:** Native support for discovering and importing EKS clusters directly from your AWS account.
* **Config Upload:** Upload new `kubeconfig` files directly via the UI.

### 📊 Visualizations & Dashboards
* **Cluster Overview:** Real-time CPU/Memory usage vs. Allocatable capacity.
* **Workload Dashboard:** Treemaps and Stacked Bar charts showing Pod distribution, health statuses, and restart loops.
* **Event Timeline:** Heatmap visualization of cluster warning events over the last hour for rapid incident correlation.

### 🔍 Deep Inspection (Read-Only)
* **Workloads:** Pods, Deployments, ReplicaSets, DaemonSets, StatefulSets.
* **Infrastructure:** Nodes (with capacity analysis) and Namespaces.
* **Networking:** Services and Ingresses (with routing rule breakdown).
* **Configuration:** ConfigMaps and Secrets (with secure value masking/unmasking).
* **Storage:** PersistentVolumeClaims (PVCs) and their mounting pods.

### 🛠️ Troubleshooting Tools
* **Live Log Streaming:** Stream container logs directly in the browser with a robust, long-lived connection.
* **Global Search:** Regex-based search across all resources in all selected clusters.
* **Link Navigation:** "Click-through" navigation (e.g., click a Pod's owner to jump to the Deployment, click a PVC to jump to the mounting Pod).

---

## 🚀 Getting Started

### Prerequisites
* **Go 1.20+** installed.
* **AWS CLI** installed and configured (if using EKS discovery).
* Access to Kubernetes clusters.

### Installation

1.  **Clone the repository:**
    ```bash
    git clone https://github.com/yourusername/k8s-universal-inspector.git
    cd k8s-universal-inspector
    ```

2.  **Install dependencies:**
    ```bash
    go mod tidy
    ```

3.  **Run the application:**
    ```bash
    go run .
    ```

4.  **Open your browser:**
    Navigate to `http://localhost:8080`.

---

## ⚙️ Configuration

### Kubeconfig Discovery
By default, the application looks for kubeconfig files in your home directory (`~/.kube/`) that match the pattern:
`config-ops*`

* **Example:** `~/.kube/config-ops-prod.yaml`
* **Example:** `~/.kube/config-ops-dev.yaml`

### AWS EKS Discovery
To import EKS clusters:
1.  Ensure your local environment has AWS credentials set (env vars or `~/.aws/credentials`).
2.  Go to the **Clusters** page in the UI.
3.  Enter your region (e.g., `us-east-1`) in the toolbar.
4.  Click **Discover**.
5.  The app will generate config files automatically and save them to your `.kube` directory.

---

## 🏗️ Project Structure

The project is built using **Go (Golang)** with **Echo** for the web server and **standard HTML templates** for the frontend. It avoids heavy frontend frameworks (React/Vue) in favor of speed and simplicity.

```text
k8s-universal-inspector/
├── main.go                 # Entry point, router setup, and template renderer
├── helpers.go              # Utility functions (formatting, search logic)
├── structs.go              # Data models for Pages and K8s resources
├── handlers_cluster.go     # Logic for Clusters, Nodes, and Config Upload
├── handlers_workload.go    # Logic for Workload Overview
├── handlers_pods.go        # Logic for Pods and Log Streaming
├── handlers_deployments.go # Logic for Deployments and ReplicaSets
├── handlers_sets.go        # Logic for DaemonSets and StatefulSets
├── handlers_network.go     # Logic for Services, Ingress, and Namespaces
├── handlers_config.go      # Logic for ConfigMaps, Secrets, and PVCs
├── handlers_search_events.go # Logic for Search and Event Heatmaps
├── handlers_aws.go         # AWS EKS Discovery logic
└── views/                  # HTML Templates
    ├── _sidebar.html       # Navigation partial
    ├── clusters.html       # Cluster list & management
    ├── overview.html       # High-level infrastructure stats
    ├── events.html         # Event heatmap & timeline
    ├── style.css           # Master stylesheet (Dark theme, responsive grid)
    └── ... (detail pages)