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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	capi "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	capv "sigs.k8s.io/cluster-api-provider-vsphere/api/govmomi/v1beta2"

	"github.com/giantswarm/cluster-api-cleaner-vsphere/pkg/cleaner"
	"github.com/giantswarm/cluster-api-cleaner-vsphere/pkg/key"
)

// TestReconcileMissingVSphereCluster covers a request for an object that is gone.
func TestReconcileMissingVSphereCluster(t *testing.T) {
	ctx := t.Context()
	namespace := newNamespace(ctx, t)

	request := ctrl.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: "does-not-exist"}}

	result, err := newReconciler(t).Reconcile(ctx, request)
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)
}

// TestReconcileSkipsFinalizer covers the guards that must run before the operator
// takes ownership of a cluster.
func TestReconcileSkipsFinalizer(t *testing.T) {
	tests := []struct {
		name        string
		withOwner   bool
		paused      bool
		annotations map[string]string
	}{
		{
			name:      "owner reference is not set yet",
			withOwner: false,
		},
		{
			name:      "owner cluster is paused",
			withOwner: true,
			paused:    true,
		},
		{
			name:        "infrastructure cluster has the paused annotation",
			withOwner:   true,
			annotations: map[string]string{capi.PausedAnnotation: ""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			namespace := newNamespace(ctx, t)

			config := vsphereClusterConfig{
				namespace:   namespace,
				name:        testClusterName,
				clusterName: testClusterName,
				annotations: tc.annotations,
			}
			if tc.withOwner {
				config.owner = newCluster(ctx, t, namespace, testClusterName, tc.paused)
			}

			vsphereCluster := createVSphereCluster(ctx, t, config)

			result, err := callReconcile(ctx, newReconciler(t), vsphereCluster)
			require.NoError(t, err)
			require.Equal(t, ctrl.Result{}, result)

			require.False(t, hasCleanerFinalizer(getVSphereCluster(ctx, t, vsphereCluster)),
				"the finalizer must not be added")
		})
	}
}

// TestReconcileNormalAddsFinalizer covers a cluster with no identity reference.
// An unrelated secret proves the reconciler does not touch secrets it was not
// pointed at.
func TestReconcileNormalAddsFinalizer(t *testing.T) {
	ctx := t.Context()
	namespace := newNamespace(ctx, t)

	owner := newCluster(ctx, t, namespace, testClusterName, false)
	unrelated := newIdentitySecret(ctx, t, namespace, "unrelated", "user", "pass")

	vsphereCluster := createVSphereCluster(ctx, t, vsphereClusterConfig{
		namespace:   namespace,
		name:        testClusterName,
		owner:       owner,
		clusterName: testClusterName,
	})

	result, err := callReconcile(ctx, newReconciler(t), vsphereCluster)
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)

	require.True(t, hasCleanerFinalizer(getVSphereCluster(ctx, t, vsphereCluster)))
	require.False(t, hasCleanerFinalizer(getSecret(ctx, t, unrelated)),
		"an unreferenced secret must not get a finalizer")
}

// TestReconcileNormalAddsFinalizerToIdentitySecret covers credential protection.
// The secret must survive until the clean-up is done.
func TestReconcileNormalAddsFinalizerToIdentitySecret(t *testing.T) {
	ctx := t.Context()
	namespace := newNamespace(ctx, t)

	owner := newCluster(ctx, t, namespace, testClusterName, false)
	secret := newIdentitySecret(ctx, t, namespace, "credentials", "user", "pass")

	vsphereCluster := createVSphereCluster(ctx, t, vsphereClusterConfig{
		namespace:   namespace,
		name:        testClusterName,
		owner:       owner,
		clusterName: testClusterName,
		identityRef: secretIdentityRef(secret.Name),
	})

	_, err := callReconcile(ctx, newReconciler(t), vsphereCluster)
	require.NoError(t, err)

	require.True(t, hasCleanerFinalizer(getVSphereCluster(ctx, t, vsphereCluster)))
	require.True(t, hasCleanerFinalizer(getSecret(ctx, t, secret)))
}

// TestReconcileNormalIsIdempotent covers repeated reconciles, which the manager
// does routinely.
func TestReconcileNormalIsIdempotent(t *testing.T) {
	ctx := t.Context()
	namespace := newNamespace(ctx, t)

	owner := newCluster(ctx, t, namespace, testClusterName, false)
	secret := newIdentitySecret(ctx, t, namespace, "credentials", "user", "pass")

	vsphereCluster := createVSphereCluster(ctx, t, vsphereClusterConfig{
		namespace:   namespace,
		name:        testClusterName,
		owner:       owner,
		clusterName: testClusterName,
		identityRef: secretIdentityRef(secret.Name),
	})

	reconciler := newReconciler(t)
	for i := range 3 {
		_, err := callReconcile(ctx, reconciler, vsphereCluster)
		require.NoError(t, err, "reconcile %d failed", i+1)
	}

	require.Equal(t, []string{key.CleanerFinalizerName},
		getVSphereCluster(ctx, t, vsphereCluster).Finalizers)
	require.Equal(t, []string{key.CleanerFinalizerName},
		getSecret(ctx, t, secret).Finalizers)
}

// TestReconcileNormalMissingIdentitySecret pins an asymmetry: the normal path
// fails on a missing secret, while the delete path tolerates it.
func TestReconcileNormalMissingIdentitySecret(t *testing.T) {
	ctx := t.Context()
	namespace := newNamespace(ctx, t)

	owner := newCluster(ctx, t, namespace, testClusterName, false)

	vsphereCluster := createVSphereCluster(ctx, t, vsphereClusterConfig{
		namespace:   namespace,
		name:        testClusterName,
		owner:       owner,
		clusterName: testClusterName,
		identityRef: secretIdentityRef("does-not-exist"),
	})

	_, err := callReconcile(ctx, newReconciler(t), vsphereCluster)
	require.Error(t, err)

	// The finalizer still lands on the cluster, because it is added first.
	require.True(t, hasCleanerFinalizer(getVSphereCluster(ctx, t, vsphereCluster)))
}

// TestReconcileNormalClusterIdentityKind pins that a VSphereClusterIdentity
// reference gets no finalizer protection, because IsSecretIdentity is false. The
// secret behind such an identity is therefore deletable mid-cleanup.
func TestReconcileNormalClusterIdentityKind(t *testing.T) {
	ctx := t.Context()
	namespace := newNamespace(ctx, t)

	owner := newCluster(ctx, t, namespace, testClusterName, false)
	secret := newIdentitySecret(ctx, t, namespace, "credentials", "user", "pass")

	vsphereCluster := createVSphereCluster(ctx, t, vsphereClusterConfig{
		namespace:   namespace,
		name:        testClusterName,
		owner:       owner,
		clusterName: testClusterName,
		identityRef: &capv.VSphereIdentityReference{
			Kind: capv.VSphereClusterIdentityKind,
			Name: "cluster-identity",
		},
	})

	_, err := callReconcile(ctx, newReconciler(t), vsphereCluster)
	require.NoError(t, err)

	require.True(t, hasCleanerFinalizer(getVSphereCluster(ctx, t, vsphereCluster)))
	require.False(t, hasCleanerFinalizer(getSecret(ctx, t, secret)),
		"a VSphereClusterIdentity secret gets no finalizer")
}

// TestReconcileDeleteWithoutCleanerFinalizer covers a deleting cluster the
// operator never took ownership of.
func TestReconcileDeleteWithoutCleanerFinalizer(t *testing.T) {
	ctx := t.Context()
	namespace := newNamespace(ctx, t)

	owner := newCluster(ctx, t, namespace, testClusterName, false)

	// A foreign finalizer keeps the object observable while it deletes. With no
	// finalizer at all the apiserver removes it immediately, so this branch would
	// be unreachable.
	const foreignFinalizer = "example.com/hold"

	vsphereCluster := createVSphereCluster(ctx, t, vsphereClusterConfig{
		namespace:   namespace,
		name:        testClusterName,
		owner:       owner,
		clusterName: testClusterName,
		finalizers:  []string{foreignFinalizer},
	})
	require.NoError(t, k8sClient.Delete(ctx, vsphereCluster))

	stub := &stubCleaner{}

	result, err := callReconcile(ctx, newReconciler(t, stub), vsphereCluster)
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)
	require.Zero(t, stub.calls, "the cleaner must not run")

	current := getVSphereCluster(ctx, t, vsphereCluster)
	require.NotNil(t, current)
	require.Equal(t, []string{foreignFinalizer}, current.Finalizers,
		"a foreign finalizer must be left alone")
}

// TestReconcileDeleteWithoutClusterNameLabel pins a latent bug.
//
// BUG: the delete path returns early when the CAPI cluster-name label is
// missing, without removing the finalizer. The object then blocks its own
// deletion forever, and nothing in the normal path guarantees the label exists.
func TestReconcileDeleteWithoutClusterNameLabel(t *testing.T) {
	ctx := t.Context()
	namespace := newNamespace(ctx, t)

	owner := newCluster(ctx, t, namespace, testClusterName, false)

	vsphereCluster := createVSphereCluster(ctx, t, vsphereClusterConfig{
		namespace: namespace,
		name:      testClusterName,
		owner:     owner,
		// No clusterName, so the label is absent.
		finalizers: []string{key.CleanerFinalizerName},
	})
	require.NoError(t, k8sClient.Delete(ctx, vsphereCluster))

	stub := &stubCleaner{}

	result, err := callReconcile(ctx, newReconciler(t, stub), vsphereCluster)
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)
	require.Zero(t, stub.calls, "the cleaner must not run")

	current := getVSphereCluster(ctx, t, vsphereCluster)
	require.NotNil(t, current, "the object is stuck, so it must still exist")
	require.True(t, hasCleanerFinalizer(current),
		"BUG: the finalizer stays, so the cluster never finishes deleting")
}

// TestReconcileDeleteRemovesFinalizers covers the happy path end to end.
func TestReconcileDeleteRemovesFinalizers(t *testing.T) {
	ctx := t.Context()

	first, second := &stubCleaner{}, &stubCleaner{}
	fixture := newDeletingCluster(ctx, t, first, second)

	result, err := callReconcile(ctx, fixture.reconciler, fixture.vsphereCluster)
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)

	require.Equal(t, 1, first.calls)
	require.Equal(t, 1, second.calls, "every cleaner must run")

	require.Nil(t, getVSphereCluster(ctx, t, fixture.vsphereCluster),
		"the last finalizer is gone, so the apiserver removed the object")

	secret := getSecret(ctx, t, fixture.secret)
	require.NotNil(t, secret)
	require.False(t, hasCleanerFinalizer(secret), "the secret must be released")
}

// TestReconcileDeleteWithMissingIdentitySecret covers a cluster whose credentials
// are already gone. Deletion must still finish rather than block on a vCenter it
// cannot reach.
func TestReconcileDeleteWithMissingIdentitySecret(t *testing.T) {
	ctx := t.Context()
	namespace := newNamespace(ctx, t)

	owner := newCluster(ctx, t, namespace, testClusterName, false)

	vsphereCluster := createVSphereCluster(ctx, t, vsphereClusterConfig{
		namespace:   namespace,
		name:        testClusterName,
		owner:       owner,
		clusterName: testClusterName,
		identityRef: secretIdentityRef("does-not-exist"),
		finalizers:  []string{key.CleanerFinalizerName},
	})
	require.NoError(t, k8sClient.Delete(ctx, vsphereCluster))

	stub := &stubCleaner{}

	result, err := callReconcile(ctx, newReconciler(t, stub), vsphereCluster)
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)
	require.Zero(t, stub.calls, "clean-up is skipped when the credentials are gone")

	require.Nil(t, getVSphereCluster(ctx, t, vsphereCluster))
}

// TestReconcileDeleteCleanerError covers a failing cleaner. The finalizers must
// survive, so the clean-up is retried instead of leaking resources.
func TestReconcileDeleteCleanerError(t *testing.T) {
	ctx := t.Context()

	stub := &stubCleaner{err: errors.New("clean-up failed")}
	fixture := newDeletingCluster(ctx, t, stub)

	_, err := callReconcile(ctx, fixture.reconciler, fixture.vsphereCluster)
	require.Error(t, err)
	require.Equal(t, 1, stub.calls)

	current := getVSphereCluster(ctx, t, fixture.vsphereCluster)
	require.NotNil(t, current)
	require.True(t, hasCleanerFinalizer(current), "the finalizer must survive a failure")
	require.True(t, hasCleanerFinalizer(getSecret(ctx, t, fixture.secret)),
		"the credentials must stay protected for the retry")
}

// TestReconcileDeleteRequeue covers a cleaner reporting unfinished work.
func TestReconcileDeleteRequeue(t *testing.T) {
	ctx := t.Context()

	done, pending := &stubCleaner{}, &stubCleaner{requeue: true}
	fixture := newDeletingCluster(ctx, t, done, pending)

	result, err := callReconcile(ctx, fixture.reconciler, fixture.vsphereCluster)
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, result.RequeueAfter)

	require.Equal(t, 1, done.calls)
	require.Equal(t, 1, pending.calls, "one requeue must not stop the other cleaners")

	current := getVSphereCluster(ctx, t, fixture.vsphereCluster)
	require.NotNil(t, current)
	require.True(t, hasCleanerFinalizer(current), "the finalizer must survive a requeue")
}

// TestReconcileDeleteStopsAtFirstError covers the loop aborting. Cleaners after a
// failing one do not run, so their work waits for the retry.
func TestReconcileDeleteStopsAtFirstError(t *testing.T) {
	ctx := t.Context()

	failing := &stubCleaner{err: errors.New("clean-up failed")}
	later := &stubCleaner{}
	fixture := newDeletingCluster(ctx, t, failing, later)

	_, err := callReconcile(ctx, fixture.reconciler, fixture.vsphereCluster)
	require.Error(t, err)

	require.Equal(t, 1, failing.calls)
	require.Zero(t, later.calls, "the loop must stop at the first error")
}

// TestReconcileDeleteToleratesSecretRemovedDuringCleanup covers the secret
// disappearing between the clean-up and the finalizer removal.
func TestReconcileDeleteToleratesSecretRemovedDuringCleanup(t *testing.T) {
	ctx := t.Context()

	stub := &stubCleaner{}
	fixture := newDeletingCluster(ctx, t, stub)

	// Delete the secret from inside the clean-up, which is the race the delete
	// path guards against.
	stub.onClean = func(ctx context.Context) error {
		secret := getSecret(ctx, t, fixture.secret)
		if secret == nil {
			return nil
		}

		secret.SetFinalizers(nil)
		if err := k8sClient.Update(ctx, secret); err != nil {
			return err
		}

		return k8sClient.Delete(ctx, secret)
	}

	result, err := callReconcile(ctx, fixture.reconciler, fixture.vsphereCluster)
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)
	require.Equal(t, 1, stub.calls)

	require.Nil(t, getSecret(ctx, t, fixture.secret))
	require.Nil(t, getVSphereCluster(ctx, t, fixture.vsphereCluster))
}

// TestReconcileDeleteReleasesSecretFirst covers the finalizer removal order. If
// the cluster were released first, a crash in between would leave the secret
// undeletable with nothing left to clean it up.
func TestReconcileDeleteReleasesSecretFirst(t *testing.T) {
	ctx := t.Context()

	fixture := newDeletingCluster(ctx, t, &stubCleaner{})

	recorder := &updateRecorder{Client: k8sClient}
	fixture.reconciler.Client = recorder

	_, err := callReconcile(ctx, fixture.reconciler, fixture.vsphereCluster)
	require.NoError(t, err)

	require.Equal(t, []string{"secret", "vspherecluster"}, recorder.updates)
}

// TestReconcileDeleteUnreachableVCenter pins a latent bug.
//
// BUG: when vCenter cannot be reached the error propagates and the finalizers
// stay, so a cluster whose vCenter is permanently gone can never be deleted
// without manual intervention.
func TestReconcileDeleteUnreachableVCenter(t *testing.T) {
	ctx := t.Context()

	stub := &stubCleaner{}
	fixture := newDeletingCluster(ctx, t, stub)

	// Closing the simulator makes the vCenter address refuse connections.
	fixture.simulator.Close()

	_, err := callReconcile(ctx, fixture.reconciler, fixture.vsphereCluster)
	require.Error(t, err)
	require.Zero(t, stub.calls, "no cleaner runs without a session")

	current := getVSphereCluster(ctx, t, fixture.vsphereCluster)
	require.NotNil(t, current)
	require.True(t, hasCleanerFinalizer(current),
		"BUG: the cluster cannot finish deleting while vCenter is unreachable")
}

// TestReconcileDeleteDeletesVolumes covers the whole operator against a
// simulator: the real volume cleaner, a real vCenter session, and a real
// apiserver. It also proves the clean-up is scoped to one cluster.
func TestReconcileDeleteDeletesVolumes(t *testing.T) {
	ctx := t.Context()

	fixture := newDeletingCluster(ctx, t, cleaner.NewVolumeCleaner(k8sClient))

	_, err := fixture.simulator.SeedVolume(ctx, fixture.vsphereCluster.Name, "pvc-owned")
	require.NoError(t, err)

	_, err = fixture.simulator.SeedVolume(ctx, "other-cluster", "pvc-other")
	require.NoError(t, err)

	result, err := callReconcile(ctx, fixture.reconciler, fixture.vsphereCluster)
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)

	require.Nil(t, getVSphereCluster(ctx, t, fixture.vsphereCluster))

	remaining, err := fixture.simulator.VolumeNames(ctx, "")
	require.NoError(t, err)
	require.Equal(t, []string{"pvc-other"}, remaining,
		"only the deleted cluster's volume may be removed")
}

// updateRecorder records the kind of each updated object, in order.
type updateRecorder struct {
	client.Client

	updates []string
}

func (c *updateRecorder) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	switch obj.(type) {
	case *v1.Secret:
		c.updates = append(c.updates, "secret")
	case *capv.VSphereCluster:
		c.updates = append(c.updates, "vspherecluster")
	default:
		c.updates = append(c.updates, fmt.Sprintf("%T", obj))
	}

	return c.Client.Update(ctx, obj, opts...)
}
