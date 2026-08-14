//go:build integration

/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	capi "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	capv "sigs.k8s.io/cluster-api-provider-vsphere/api/govmomi/v1beta2"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/identity"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/session"

	"github.com/giantswarm/cluster-api-cleaner-vsphere/internal/vcsim"
	"github.com/giantswarm/cluster-api-cleaner-vsphere/pkg/cleaner"
	"github.com/giantswarm/cluster-api-cleaner-vsphere/pkg/key"
)

// testClusterName is the name shared by the Cluster and the VSphereCluster in
// every test. The cleaner scopes its work by cluster name, so the two must match.
const testClusterName = "test-cluster"

// k8sClient talks to the envtest apiserver. It is shared by every test in the
// package, so tests isolate themselves with a namespace each.
var k8sClient client.Client

// namespaceCounter names namespaces uniquely within a run.
var namespaceCounter atomic.Int64

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	paths, err := crdPaths()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	testEnv := &envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{Paths: paths},
	}

	cfg, err := testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start envtest: %s\n"+
			"Run the suite with 'make integration-test', which sets KUBEBUILDER_ASSETS.\n", err)
		return 1
	}

	defer func() {
		if err := testEnv.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to stop envtest: %s\n", err)
		}
	}()

	scheme := runtime.NewScheme()
	for _, addToScheme := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		capi.AddToScheme,
		capv.AddToScheme,
	} {
		if err := addToScheme(scheme); err != nil {
			fmt.Fprintf(os.Stderr, "failed to build scheme: %s\n", err)
			return 1
		}
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create client: %s\n", err)
		return 1
	}

	return m.Run()
}

// crdPaths locates the CAPI and CAPV CRDs in the module cache. The operator
// defines no API types of its own, so it ships no CRDs to install.
func crdPaths() ([]string, error) {
	modules := []struct {
		path string
		crd  string
	}{
		{"sigs.k8s.io/cluster-api", "config/crd/bases/cluster.x-k8s.io_clusters.yaml"},
		// Note the extra "default" segment, which CAPI does not have.
		{"sigs.k8s.io/cluster-api-provider-vsphere", "config/default/crd/bases/infrastructure.cluster.x-k8s.io_vsphereclusters.yaml"},
	}

	paths := make([]string, 0, len(modules))
	for _, module := range modules {
		// The module path is a constant from the slice above, not user input.
		out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module.path).Output() //nolint:gosec
		if err != nil {
			return nil, fmt.Errorf("failed to locate module %s: %w", module.path, err)
		}

		dir := strings.TrimSpace(string(out))
		if dir == "" {
			return nil, fmt.Errorf("module %s is not in the module cache, run 'go mod download'", module.path)
		}

		paths = append(paths, filepath.Join(dir, module.crd))
	}

	return paths, nil
}

// newNamespace creates a namespace for a single test.
//
// envtest runs no namespace controller, so namespaces are never deleted. Every
// test gets a fresh one instead of cleaning up after itself.
func newNamespace(ctx context.Context, t *testing.T) string {
	t.Helper()

	name := fmt.Sprintf("test-%d", namespaceCounter.Add(1))
	namespace := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	require.NoError(t, k8sClient.Create(ctx, namespace))

	return name
}

// newCluster creates the owner Cluster that the reconciler looks up.
//
// Paused is always set, even when false. ClusterSpec is tagged omitzero and the
// CRD requires spec, so a fully zero spec is dropped from the request body and
// the apiserver rejects the object.
func newCluster(ctx context.Context, t *testing.T, namespace, name string, paused bool) *capi.Cluster {
	t.Helper()

	cluster := &capi.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       capi.ClusterSpec{Paused: ptr.To(paused)},
	}
	require.NoError(t, k8sClient.Create(ctx, cluster))

	return cluster
}

// newIdentitySecret creates a vCenter credentials secret. identity.GetCredentials
// reads Data, not StringData.
func newIdentitySecret(ctx context.Context, t *testing.T, namespace, name, username, password string) *v1.Secret {
	t.Helper()

	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data: map[string][]byte{
			identity.UsernameKey: []byte(username),
			identity.PasswordKey: []byte(password),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, secret))

	return secret
}

// vsphereClusterConfig describes a VSphereCluster to create.
type vsphereClusterConfig struct {
	namespace string
	name      string

	// owner is the Cluster to reference. A nil owner leaves the ownerRef unset,
	// which is what the reconciler treats as "not yet set".
	owner *capi.Cluster

	// clusterName sets the CAPI cluster-name label. An empty value omits the
	// label, which the delete path requires.
	clusterName string

	// identityRef is optional. The CRD requires both kind and name when present.
	identityRef *capv.VSphereIdentityReference

	// server defaults to a dummy address for tests that never reach vCenter.
	// The CRD requires it.
	server string

	finalizers  []string
	annotations map[string]string
}

func createVSphereCluster(ctx context.Context, t *testing.T, config vsphereClusterConfig) *capv.VSphereCluster {
	t.Helper()

	if config.server == "" {
		config.server = "vcenter.example.com"
	}

	vsphereCluster := &capv.VSphereCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        config.name,
			Namespace:   config.namespace,
			Finalizers:  config.finalizers,
			Annotations: config.annotations,
		},
		Spec: capv.VSphereClusterSpec{Server: config.server},
	}

	if config.clusterName != "" {
		vsphereCluster.Labels = map[string]string{key.CapiClusterLabelKey: config.clusterName}
	}

	if config.identityRef != nil {
		vsphereCluster.Spec.IdentityRef = *config.identityRef
	}

	if config.owner != nil {
		vsphereCluster.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: capi.GroupVersion.String(),
			Kind:       "Cluster",
			Name:       config.owner.Name,
			UID:        config.owner.UID,
		}}
	}

	require.NoError(t, k8sClient.Create(ctx, vsphereCluster))

	return vsphereCluster
}

// secretIdentityRef builds an IdentityRef of kind Secret.
func secretIdentityRef(name string) *capv.VSphereIdentityReference {
	return &capv.VSphereIdentityReference{Kind: capv.SecretKind, Name: name}
}

func newReconciler(t *testing.T, cleaners ...cleaner.Cleaner) *VSphereClusterReconciler {
	t.Helper()

	return &VSphereClusterReconciler{
		Client:   k8sClient,
		Log:      testr.New(t),
		Cleaners: cleaners,
	}
}

// callReconcile calls Reconcile directly rather than starting a manager. That
// keeps results and cleaner call counts exactly assertable, with no polling.
//
// It is not named reconcile, because the package under test already imports a
// package by that name.
func callReconcile(ctx context.Context, r *VSphereClusterReconciler, obj client.Object) (ctrl.Result, error) {
	return r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(obj)})
}

// getVSphereCluster re-reads a VSphereCluster. It returns nil when the object is
// gone, which is how the tests assert that the last finalizer was removed.
func getVSphereCluster(ctx context.Context, t *testing.T, obj client.Object) *capv.VSphereCluster {
	t.Helper()

	vsphereCluster := &capv.VSphereCluster{}
	err := k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), vsphereCluster)
	if err != nil {
		require.True(t, apierrors.IsNotFound(err), "unexpected error reading VSphereCluster: %s", err)
		return nil
	}

	return vsphereCluster
}

// getSecret re-reads a secret, returning nil when it is gone.
func getSecret(ctx context.Context, t *testing.T, obj client.Object) *v1.Secret {
	t.Helper()

	secret := &v1.Secret{}
	err := k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), secret)
	if err != nil {
		require.True(t, apierrors.IsNotFound(err), "unexpected error reading secret: %s", err)
		return nil
	}

	return secret
}

// hasCleanerFinalizer reports whether the object carries the operator finalizer.
func hasCleanerFinalizer(obj client.Object) bool {
	return controllerutil.ContainsFinalizer(obj, key.CleanerFinalizerName)
}

// newSimulator starts a vCenter simulator scoped to one test.
func newSimulator(t *testing.T) *vcsim.Simulator {
	t.Helper()

	simulator, err := vcsim.New()
	require.NoError(t, err)
	t.Cleanup(simulator.Close)

	return simulator
}

// deletingCluster is a VSphereCluster part way through deletion: the finalizers
// are installed, the deletion timestamp is set, and Spec.Server points at a live
// simulator so the real getVCenterSession succeeds.
type deletingCluster struct {
	reconciler     *VSphereClusterReconciler
	vsphereCluster *capv.VSphereCluster
	secret         *v1.Secret
	simulator      *vcsim.Simulator
}

func newDeletingCluster(ctx context.Context, t *testing.T, cleaners ...cleaner.Cleaner) *deletingCluster {
	t.Helper()

	simulator := newSimulator(t)
	namespace := newNamespace(ctx, t)
	owner := newCluster(ctx, t, namespace, testClusterName, false)
	secret := newIdentitySecret(ctx, t, namespace, "credentials", simulator.Username(), simulator.Password())

	vsphereCluster := createVSphereCluster(ctx, t, vsphereClusterConfig{
		namespace:   namespace,
		name:        testClusterName,
		owner:       owner,
		clusterName: testClusterName,
		identityRef: secretIdentityRef(secret.Name),
		server:      simulator.Host(),
	})

	reconciler := newReconciler(t, cleaners...)

	// Reconcile once to install the finalizers, then start the deletion.
	_, err := callReconcile(ctx, reconciler, vsphereCluster)
	require.NoError(t, err)
	require.NoError(t, k8sClient.Delete(ctx, vsphereCluster))

	return &deletingCluster{
		reconciler:     reconciler,
		vsphereCluster: vsphereCluster,
		secret:         secret,
		simulator:      simulator,
	}
}

// stubCleaner stands in for a real cleaner, recording its calls.
type stubCleaner struct {
	calls   int
	requeue bool
	err     error

	// onClean runs before the configured result is returned, letting a test
	// mutate the cluster mid-cleanup.
	onClean func(ctx context.Context) error
}

var _ cleaner.Cleaner = &stubCleaner{}

func (c *stubCleaner) Clean(ctx context.Context, _ logr.Logger, _ *session.Session, _ *capv.VSphereCluster) (bool, error) {
	c.calls++

	if c.onClean != nil {
		if err := c.onClean(ctx); err != nil {
			return false, err
		}
	}

	return c.requeue, c.err
}
