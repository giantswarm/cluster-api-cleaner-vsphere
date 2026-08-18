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

// Package vcsim starts an in-process vCenter simulator for tests. It registers
// the CNS endpoint, so tests can seed and query container volumes.
//
// This package is not build-tagged. A package whose every file is excluded by a
// build constraint breaks a plain "go test ./...". Nothing in the shipped binary
// imports it.
package vcsim

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/cns"
	cnstypes "github.com/vmware/govmomi/cns/types"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"
	"github.com/vmware/govmomi/vslm"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/session"

	_ "github.com/vmware/govmomi/cns/simulator"  // registers the CNS endpoint
	_ "github.com/vmware/govmomi/vapi/simulator" // registers the vAPI endpoints, needed by session.GetOrCreate
)

// Simulator is a running vCenter simulator.
type Simulator struct {
	model  *simulator.Model
	server *simulator.Server
	close  sync.Once
}

// New starts a simulator. The caller must call Close.
//
// Use one Simulator per test. Each instance binds a random port, and
// session.GetOrCreate keys its process-global cache on the server address, so a
// fresh port is what keeps tests isolated from each other.
func New() (*Simulator, error) {
	// The CNS and vAPI endpoints only register against a VPX model.
	model := simulator.VPX()

	if err := model.Create(); err != nil {
		return nil, fmt.Errorf("failed to create simulator model: %w", err)
	}

	// session.GetOrCreate builds an https URL, so TLS is mandatory. httptest
	// supplies a default certificate when Certificates is empty.
	model.Service.TLS = &tls.Config{MinVersion: tls.VersionTLS12}

	// Invokes the endpoints registered by the blank imports above.
	model.Service.RegisterEndpoints = true

	return &Simulator{model: model, server: model.Service.NewServer()}, nil
}

// Close shuts the simulator down and removes its temporary files. It is safe to
// call more than once, so a test can close the simulator early to make vCenter
// unreachable and still leave its cleanup registered.
func (s *Simulator) Close() {
	s.close.Do(func() {
		s.server.Close()
		s.model.Remove()
	})
}

// Host returns the "host:port" address to put in VSphereCluster.Spec.Server.
func (s *Simulator) Host() string {
	return s.server.URL.Host
}

// Username returns the username the simulator accepts.
func (s *Simulator) Username() string {
	return simulator.DefaultLogin.Username()
}

// Password returns the password the simulator accepts.
func (s *Simulator) Password() string {
	password, _ := simulator.DefaultLogin.Password()
	return password
}

// Session returns a session pointing at the simulator.
//
// It builds the Session directly rather than calling session.GetOrCreate, so it
// neither needs a datacenter nor writes to that function's global cache.
func (s *Simulator) Session(ctx context.Context) (*session.Session, error) {
	client, err := govmomi.NewClient(ctx, s.server.URL, true)
	if err != nil {
		return nil, fmt.Errorf("failed to create govmomi client: %w", err)
	}

	return &session.Session{Client: client}, nil
}

// SeedVolume creates a CNS container volume owned by clusterID and returns its
// volume ID. The cleaner finds volumes by matching clusterID, so this is what
// makes a volume visible to it.
func (s *Simulator) SeedVolume(ctx context.Context, clusterID, name string) (string, error) {
	client, err := s.cnsClient(ctx)
	if err != nil {
		return "", err
	}

	datastore := s.model.Map().Any("Datastore").(*simulator.Datastore)

	spec := cnstypes.CnsVolumeCreateSpec{
		Name:       name,
		VolumeType: string(cnstypes.CnsVolumeTypeBlock),
		Datastores: []types.ManagedObjectReference{datastore.Self},
		Metadata: cnstypes.CnsVolumeMetadata{
			ContainerCluster: cnstypes.CnsContainerCluster{
				ClusterType:   string(cnstypes.CnsClusterTypeKubernetes),
				ClusterId:     clusterID,
				ClusterFlavor: string(cnstypes.CnsClusterFlavorVanilla),
				VSphereUser:   s.Username(),
			},
		},
		BackingObjectDetails: &cnstypes.CnsBlockBackingDetails{
			CnsBackingObjectDetails: cnstypes.CnsBackingObjectDetails{CapacityInMb: 1024},
		},
	}

	task, err := client.CreateVolume(ctx, []cnstypes.CnsVolumeCreateSpec{spec})
	if err != nil {
		return "", fmt.Errorf("failed to start volume creation: %w", err)
	}

	info, err := task.WaitForResultEx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create volume: %w", err)
	}

	result, ok := info.Result.(cnstypes.CnsVolumeOperationBatchResult)
	if !ok {
		return "", fmt.Errorf("unexpected create result type %T", info.Result)
	}

	if len(result.VolumeResults) != 1 {
		return "", fmt.Errorf("expected 1 volume result, got %d", len(result.VolumeResults))
	}

	operationResult := result.VolumeResults[0].GetCnsVolumeOperationResult()
	if operationResult.Fault != nil {
		return "", fmt.Errorf("failed to create volume: %s", operationResult.Fault.LocalizedMessage)
	}

	return operationResult.VolumeId.Id, nil
}

// VolumeNames returns the names of the volumes owned by clusterID. An empty
// clusterID returns every volume.
func (s *Simulator) VolumeNames(ctx context.Context, clusterID string) ([]string, error) {
	client, err := s.cnsClient(ctx)
	if err != nil {
		return nil, err
	}

	filter := cnstypes.CnsQueryFilter{}
	if clusterID != "" {
		filter.ContainerClusterIds = []string{clusterID}
	}

	result, err := client.QueryVolume(ctx, &filter)
	if err != nil {
		return nil, fmt.Errorf("failed to query volumes: %w", err)
	}

	names := make([]string, 0, len(result.Volumes))
	for _, volume := range result.Volumes {
		names = append(names, volume.Name)
	}

	return names, nil
}

// DeleteBackingDisk removes the first class disk behind a volume, leaving the
// CNS volume record in place. A later delete of that volume then reports a fault
// in its batch result, which is otherwise unreachable.
func (s *Simulator) DeleteBackingDisk(ctx context.Context, volumeID string) error {
	client, err := govmomi.NewClient(ctx, s.server.URL, true)
	if err != nil {
		return fmt.Errorf("failed to create govmomi client: %w", err)
	}

	datastore := s.model.Map().Any("Datastore").(*simulator.Datastore)

	task, err := vslm.NewObjectManager(client.Client).Delete(ctx, datastore, volumeID)
	if err != nil {
		return fmt.Errorf("failed to start disk deletion: %w", err)
	}

	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("failed to delete disk: %w", err)
	}

	return nil
}

func (s *Simulator) cnsClient(ctx context.Context) (*cns.Client, error) {
	client, err := govmomi.NewClient(ctx, s.server.URL, true)
	if err != nil {
		return nil, fmt.Errorf("failed to create govmomi client: %w", err)
	}

	cnsClient, err := cns.NewClient(ctx, client.Client)
	if err != nil {
		return nil, fmt.Errorf("failed to create CNS client: %w", err)
	}

	return cnsClient, nil
}
