package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/runnercmd"
	"gopkg.in/yaml.v3"
)

const serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// KubeCredential connects to a cluster (a container group's credential); empty means in-cluster.
type KubeCredential struct {
	Host, BearerToken, CACert string
	VerifySSL                 bool
}

// PodRequest describes a job pod for a container group.
type PodRequest struct {
	JobID      int64
	Spec       WorkSpec
	PodSpec    string // instance group pod_spec_override (YAML or JSON)
	Credential *KubeCredential
	// ServiceURL is how the pod reaches the control plane.
	ServiceURL string
	ShimImage  string
	InstallID  string
	// MountPaths are AWX_ISOLATION_SHOW_PATHS mounted from the node as
	// hostPath volumes (AWX_MOUNT_ISOLATED_PATHS_ON_K8S).
	MountPaths []string
}

type kubeClient struct {
	host, token string
	http        *http.Client
}

func newKubeClient(cred *KubeCredential) (*kubeClient, error) {
	tlsCfg := &tls.Config{}
	k := &kubeClient{}
	if cred == nil || cred.Host == "" {
		host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
		if host == "" {
			return nil, errors.New("not running in a cluster and the container group has no OpenShift or Kubernetes API Bearer Token credential")
		}
		tok, err := os.ReadFile(serviceAccountDir + "/token")
		if err != nil {
			return nil, err
		}
		ca, err := os.ReadFile(serviceAccountDir + "/ca.crt")
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(ca)
		tlsCfg.RootCAs = pool
		k.host, k.token = "https://"+host+":"+port, strings.TrimSpace(string(tok))
	} else {
		k.host, k.token = strings.TrimRight(cred.Host, "/"), cred.BearerToken
		if !cred.VerifySSL {
			tlsCfg.InsecureSkipVerify = true //nolint:gosec // the credential disables verification
		} else if cred.CACert != "" {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM([]byte(cred.CACert))
			tlsCfg.RootCAs = pool
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsCfg
	k.http = &http.Client{Transport: tr, Timeout: 30 * time.Second}
	return k, nil
}

func (k *kubeClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, k.host+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		var st struct {
			Message string `json:"message"`
		}
		json.Unmarshal(b, &st)
		if st.Message == "" {
			st.Message = strings.TrimSpace(string(b))
		}
		return fmt.Errorf("kubernetes %s %s: %s: %s", method, path, resp.Status, st.Message)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func inClusterNamespace() string {
	if b, err := os.ReadFile(serviceAccountDir + "/namespace"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return "default"
}

// BuildPod renders the job pod: the container group's pod spec with the
// execution environment as the "worker" container, started through the shim.
func BuildPod(req PodRequest, unitURL, token string) (map[string]any, string, error) {
	pod := map[string]any{}
	if strings.TrimSpace(req.PodSpec) != "" {
		if err := yaml.Unmarshal([]byte(req.PodSpec), &pod); err != nil {
			return nil, "", fmt.Errorf("invalid pod_spec_override: %w", err)
		}
	}
	pod = normalizeYAML(pod).(map[string]any)
	pod["apiVersion"], pod["kind"] = "v1", "Pod"
	meta, _ := pod["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	ns, _ := meta["namespace"].(string)
	if ns == "" {
		ns = inClusterNamespace()
	}
	meta["namespace"] = ns
	delete(meta, "name")
	meta["generateName"] = fmt.Sprintf("automation-job-%d-", req.JobID)
	labels, _ := meta["labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	labels["ansible-awx"] = req.InstallID
	labels["ansible-awx-job-id"] = fmt.Sprint(req.JobID)
	meta["labels"] = labels
	pod["metadata"] = meta

	spec, _ := pod["spec"].(map[string]any)
	if spec == nil {
		spec = map[string]any{"serviceAccountName": "default", "automountServiceAccountToken": false}
	}
	spec["restartPolicy"] = "Never"
	containers, _ := spec["containers"].([]any)
	worker := map[string]any{}
	if len(containers) > 0 {
		if c, ok := containers[0].(map[string]any); ok {
			worker = c
		}
	}
	if _, ok := worker["resources"]; !ok {
		worker["resources"] = map[string]any{"requests": map[string]any{"cpu": "250m", "memory": "100Mi"}}
	}
	worker["name"] = "worker"
	worker["image"] = req.Spec.Image
	delete(worker, "args")
	worker["command"] = []any{"/awxnext-shim/awxnext", "shim", "--url", unitURL}
	env, _ := worker["env"].([]any)
	env = append(env, map[string]any{"name": "AWXNEXT_SHIM_TOKEN", "value": token}, map[string]any{"name": "HOME", "value": "/runner"})
	worker["env"] = env
	mounts, _ := worker["volumeMounts"].([]any)
	mounts = append(mounts, map[string]any{"name": "awxnext-shim", "mountPath": "/awxnext-shim"})
	volumes, _ := spec["volumes"].([]any)
	for i, p := range req.MountPaths {
		src, dst, opts, err := runnercmd.Mount(p)
		if err != nil {
			return nil, "", err
		}
		name := fmt.Sprintf("awxnext-show-%d", i)
		volumes = append(volumes, map[string]any{"name": name, "hostPath": map[string]any{"path": src}})
		mounts = append(mounts, map[string]any{"name": name, "mountPath": dst, "readOnly": slices.Contains(strings.Split(opts, ","), "ro")})
	}
	worker["volumeMounts"] = mounts
	switch strings.ToLower(req.Spec.Pull) {
	case "always":
		worker["imagePullPolicy"] = "Always"
	case "never":
		worker["imagePullPolicy"] = "Never"
	case "missing":
		worker["imagePullPolicy"] = "IfNotPresent"
	}
	if len(containers) > 0 {
		containers[0] = worker
	} else {
		containers = []any{worker}
	}
	spec["containers"] = containers
	inits, _ := spec["initContainers"].([]any)
	spec["initContainers"] = append(inits, map[string]any{
		// Both of awxnext's images have the binary there.
		"name": "awxnext-shim", "image": req.ShimImage, "command": []any{"/usr/local/bin/awxnext", "install-shim", "/awxnext-shim/awxnext"},
		"volumeMounts": []any{map[string]any{"name": "awxnext-shim", "mountPath": "/awxnext-shim"}},
	})
	spec["volumes"] = append(volumes, map[string]any{"name": "awxnext-shim", "emptyDir": map[string]any{}})
	pod["spec"] = spec
	return pod, ns, nil
}

// normalizeYAML converts yaml.v3 decoded maps to JSON-compatible values.
func normalizeYAML(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = normalizeYAML(e)
		}
		return x
	case map[any]any:
		out := map[string]any{}
		for k, e := range x {
			out[fmt.Sprint(k)] = normalizeYAML(e)
		}
		return out
	case []any:
		for i, e := range x {
			x[i] = normalizeYAML(e)
		}
	}
	return v
}

type podWorker struct {
	*unit
	kube          *kubeClient
	ns, pod, pull string
}

func (p *podWorker) Wait() (string, error) {
	diag, err := p.unit.Wait()
	p.cleanup()
	return diag, err
}

func (p *podWorker) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if os.Getenv("AWXNEXT_KEEP_JOB_PODS") == "" {
		p.kube.do(ctx, "DELETE", "/api/v1/namespaces/"+p.ns+"/pods/"+p.pod, nil, nil)
	}
	if p.pull != "" {
		p.kube.do(ctx, "DELETE", "/api/v1/namespaces/"+p.ns+"/secrets/"+p.pull, nil, nil)
	}
}

// StartPod creates a job pod in a container group and waits for its shim to connect.
func (b *Broker) StartPod(ctx context.Context, req PodRequest) (Worker, error) {
	kube, err := newKubeClient(req.Credential)
	if err != nil {
		return nil, err
	}
	token := NewToken()
	u := b.newUnit("", token, req.Spec)
	unitURL := strings.TrimRight(req.ServiceURL, "/") + u.path(b.node)
	pod, ns, err := BuildPod(req, unitURL, token)
	if err != nil {
		b.drop(u)
		return nil, err
	}
	pw := &podWorker{unit: u, kube: kube, ns: ns}
	if req.Spec.RegistryUser != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(req.Spec.RegistryUser + ":" + req.Spec.RegistryPassword))
		cfg, _ := json.Marshal(map[string]any{"auths": map[string]any{req.Spec.RegistryHost: map[string]string{"auth": auth}}})
		pw.pull = fmt.Sprintf("automation-%d-image-pull-secret", req.JobID)
		secret := map[string]any{"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/dockerconfigjson",
			"metadata": map[string]any{"name": pw.pull, "namespace": ns},
			"data":     map[string]any{".dockerconfigjson": base64.StdEncoding.EncodeToString(cfg)}}
		kube.do(ctx, "DELETE", "/api/v1/namespaces/"+ns+"/secrets/"+pw.pull, nil, nil)
		if err := kube.do(ctx, "POST", "/api/v1/namespaces/"+ns+"/secrets", secret, nil); err != nil {
			b.drop(u)
			return nil, err
		}
		spec := pod["spec"].(map[string]any)
		secrets, _ := spec["imagePullSecrets"].([]any)
		spec["imagePullSecrets"] = append(secrets, map[string]any{"name": pw.pull})
	}
	var created struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if err := kube.do(ctx, "POST", "/api/v1/namespaces/"+ns+"/pods", pod, &created); err != nil {
		b.drop(u)
		pw.cleanup()
		return nil, err
	}
	pw.pod = created.Metadata.Name
	slog.Info("job pod created", "job", req.JobID, "pod", ns+"/"+pw.pod)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	deadline := time.After(10 * time.Minute)
	for {
		select {
		case <-u.connected:
			return pw, nil
		case <-ctx.Done():
			b.drop(u)
			pw.cleanup()
			return nil, ctx.Err()
		case <-deadline:
			b.drop(u)
			pw.cleanup()
			return nil, errors.New("job pod did not start within 10 minutes")
		case <-t.C:
			if reason := podProblem(ctx, kube, ns, pw.pod); reason != "" {
				b.drop(u)
				pw.cleanup()
				return nil, fmt.Errorf("job pod %s failed to start: %s", pw.pod, reason)
			}
		}
	}
}

// podProblem reports why a pod cannot run (image pull errors, failed init, ...).
func podProblem(ctx context.Context, kube *kubeClient, ns, name string) string {
	var p struct {
		Status struct {
			Phase                 string `json:"phase"`
			Message               string `json:"message"`
			ContainerStatuses     []containerStatus
			InitContainerStatuses []containerStatus
		} `json:"status"`
	}
	if err := kube.do(ctx, "GET", "/api/v1/namespaces/"+ns+"/pods/"+name, nil, &p); err != nil {
		return ""
	}
	if p.Status.Phase == "Failed" || p.Status.Phase == "Succeeded" {
		return strings.TrimSpace("pod " + strings.ToLower(p.Status.Phase) + " " + p.Status.Message)
	}
	for _, cs := range append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...) {
		if w := cs.State.Waiting; w != nil {
			switch w.Reason {
			case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError", "CreateContainerError", "CrashLoopBackOff":
				return cs.Name + ": " + w.Reason + ": " + w.Message
			}
		}
		if t := cs.State.Terminated; t != nil && t.ExitCode != 0 {
			return fmt.Sprintf("%s exited with code %d: %s", cs.Name, t.ExitCode, t.Message)
		}
	}
	return ""
}

type containerStatus struct {
	Name  string `json:"name"`
	State struct {
		Waiting *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"waiting"`
		Terminated *struct {
			ExitCode int    `json:"exitCode"`
			Message  string `json:"message"`
		} `json:"terminated"`
	} `json:"state"`
}
