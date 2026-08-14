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

package cleaner

import (
	"context"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	capv "sigs.k8s.io/cluster-api-provider-vsphere/api/govmomi/v1beta2"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/session"

	"github.com/giantswarm/cluster-api-cleaner-vsphere/internal/vcsim"
)

// The cleaner takes a client only to satisfy its constructor. It reads nothing
// from Kubernetes, so the tests pass a nil client.
func newTestCleaner() *VolumeCleaner {
	return NewVolumeCleaner(nil)
}

// newTestSimulator starts a simulator and returns it with a session pointing at
// it. The session is built directly, so it stays out of the process-global cache
// in session.GetOrCreate.
func newTestSimulator(ctx context.Context, t *testing.T) (*vcsim.Simulator, *session.Session) {
	t.Helper()

	simulator, err := vcsim.New()
	require.NoError(t, err)
	t.Cleanup(simulator.Close)

	sess, err := simulator.Session(ctx)
	require.NoError(t, err)

	return simulator, sess
}

func newVSphereCluster(name string) *capv.VSphereCluster {
	return &capv.VSphereCluster{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

// TestCleanDeletesVolumes covers the ordinary case.
func TestCleanDeletesVolumes(t *testing.T) {
	ctx := t.Context()
	simulator, sess := newTestSimulator(ctx, t)

	_, err := simulator.SeedVolume(ctx, "test-cluster", "pvc-owned")
	require.NoError(t, err)

	requeue, err := newTestCleaner().Clean(ctx, testr.New(t), sess, newVSphereCluster("test-cluster"))
	require.NoError(t, err)
	require.False(t, requeue)

	remaining, err := simulator.VolumeNames(ctx, "")
	require.NoError(t, err)
	require.Empty(t, remaining)
}

// TestCleanIgnoresOtherClusters is the guard against destroying another
// cluster's data. The cleaner must only touch volumes tagged with its own
// cluster ID.
func TestCleanIgnoresOtherClusters(t *testing.T) {
	ctx := t.Context()
	simulator, sess := newTestSimulator(ctx, t)

	_, err := simulator.SeedVolume(ctx, "test-cluster", "pvc-owned")
	require.NoError(t, err)

	_, err = simulator.SeedVolume(ctx, "other-cluster", "pvc-other")
	require.NoError(t, err)

	_, err = simulator.SeedVolume(ctx, "", "pvc-unowned")
	require.NoError(t, err)

	requeue, err := newTestCleaner().Clean(ctx, testr.New(t), sess, newVSphereCluster("test-cluster"))
	require.NoError(t, err)
	require.False(t, requeue)

	remaining, err := simulator.VolumeNames(ctx, "")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"pvc-other", "pvc-unowned"}, remaining,
		"volumes belonging to other clusters must survive")
}

// TestCleanWithoutVolumes covers a cluster that never provisioned storage.
func TestCleanWithoutVolumes(t *testing.T) {
	ctx := t.Context()
	_, sess := newTestSimulator(ctx, t)

	requeue, err := newTestCleaner().Clean(ctx, testr.New(t), sess, newVSphereCluster("test-cluster"))
	require.NoError(t, err)
	require.False(t, requeue)
}

// TestCleanDeletesEveryVolume covers a cluster with more than one volume, since
// the cleaner deletes them one at a time.
func TestCleanDeletesEveryVolume(t *testing.T) {
	ctx := t.Context()
	simulator, sess := newTestSimulator(ctx, t)

	for _, name := range []string{"pvc-one", "pvc-two", "pvc-three"} {
		_, err := simulator.SeedVolume(ctx, "test-cluster", name)
		require.NoError(t, err)
	}

	requeue, err := newTestCleaner().Clean(ctx, testr.New(t), sess, newVSphereCluster("test-cluster"))
	require.NoError(t, err)
	require.False(t, requeue)

	remaining, err := simulator.VolumeNames(ctx, "")
	require.NoError(t, err)
	require.Empty(t, remaining)
}

// TestCleanQueryError covers an unreachable vCenter.
func TestCleanQueryError(t *testing.T) {
	ctx := t.Context()
	simulator, sess := newTestSimulator(ctx, t)

	simulator.Close()

	requeue, err := newTestCleaner().Clean(ctx, testr.New(t), sess, newVSphereCluster("test-cluster"))
	require.Error(t, err)
	require.False(t, requeue)
}

// TestCleanDeleteFault covers a delete that reports a fault in its batch result
// rather than failing outright. Removing the backing disk behind a volume makes
// the delete fail while the volume record still exists.
func TestCleanDeleteFault(t *testing.T) {
	ctx := t.Context()
	simulator, sess := newTestSimulator(ctx, t)

	volumeID, err := simulator.SeedVolume(ctx, "test-cluster", "pvc-owned")
	require.NoError(t, err)

	require.NoError(t, simulator.DeleteBackingDisk(ctx, volumeID))

	requeue, err := newTestCleaner().Clean(ctx, testr.New(t), sess, newVSphereCluster("test-cluster"))
	require.ErrorContains(t, err, "error while deleting volume")
	require.False(t, requeue)
}
