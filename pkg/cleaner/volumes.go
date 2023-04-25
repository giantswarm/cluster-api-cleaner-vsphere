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
	"fmt"

	"github.com/go-logr/logr"
	"github.com/vmware/govmomi/cns"
	cnstypes "github.com/vmware/govmomi/cns/types"
	capv "sigs.k8s.io/cluster-api-provider-vsphere/apis/v1beta1"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/session"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type VolumeCleaner struct {
	cli client.Client
}

func NewVolumeCleaner(cli client.Client) *VolumeCleaner {
	return &VolumeCleaner{cli: cli}
}

// force implementing Cleaner interface
var _ Cleaner = &VolumeCleaner{}

func (vc *VolumeCleaner) Clean(ctx context.Context, log logr.Logger, sess *session.Session, c *capv.VSphereCluster) (bool, error) {
	log = log.WithName("VolumeCleaner")

	cnsClient, err := cns.NewClient(ctx, sess.Client.Client)
	if err != nil {
		return false, err
	}

	filter := cnstypes.CnsQueryFilter{ContainerClusterIds: []string{c.Name}}

	result, err := cnsClient.QueryVolume(ctx, filter)
	if err != nil {
		return false, err
	}

	for _, volume := range result.Volumes {
		log.Info(fmt.Sprintf("Deleting volume:[%s]", volume.Name))
		task, err := cnsClient.DeleteVolume(ctx, []cnstypes.CnsVolumeId{{Id: volume.VolumeId.Id}}, true)
		err = task.Wait(ctx)
		if err != nil {
			return false, err
		}
	}
	return false, nil
}
