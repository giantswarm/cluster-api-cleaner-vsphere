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
	"time"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	capv "sigs.k8s.io/cluster-api-provider-vsphere/apis/v1beta1"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/identity"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/session"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/giantswarm/microerror"

	"github.com/giantswarm/cluster-api-cleaner-vsphere/pkg/cleaner"
	"github.com/giantswarm/cluster-api-cleaner-vsphere/pkg/key"
)

// VSphereClusterReconciler reconciles a VSphereCluster object
type VSphereClusterReconciler struct {
	client.Client
	Log logr.Logger

	ManagementCluster string
	Cleaners          []cleaner.Cleaner
}

// +kubebuilder:rbac:groups=,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=vsphereclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=vsphereclusters/status,verbs=get;update;patch

func (r *VSphereClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("vspherecluster", req.NamespacedName)
	log.V(1).Info("Reconciling")

	var infraCluster capv.VSphereCluster
	err := r.Get(ctx, req.NamespacedName, &infraCluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, microerror.Mask(err)
	}

	// Fetch the owner cluster.
	coreCluster, err := util.GetOwnerCluster(ctx, r.Client, infraCluster.ObjectMeta)
	if err != nil {
		return reconcile.Result{}, microerror.Mask(err)
	}
	if coreCluster == nil {
		log.Info("Cluster Controller has not yet set OwnerRef")
		return reconcile.Result{}, nil
	}

	log = log.WithValues("cluster", coreCluster.Name)

	// Return early if the core or infrastructure cluster is paused.
	if annotations.IsPaused(coreCluster, &infraCluster) {
		log.Info("infrastructure or core cluster is marked as paused. Won't reconcile")
		return ctrl.Result{}, nil
	}

	// Handle deleted clusters
	if !infraCluster.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, log, &infraCluster)
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(ctx, log, &infraCluster)
}

func (r *VSphereClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&capv.VSphereCluster{}).
		Complete(r)
}

func (r *VSphereClusterReconciler) reconcileNormal(ctx context.Context, log logr.Logger, vsphereCluster *capv.VSphereCluster) (reconcile.Result, error) {
	// If the vsphereCluster doesn't have the finalizer, add it.
	err := r.addFinalizer(ctx, log, vsphereCluster)
	if err != nil {
		return reconcile.Result{}, microerror.Mask(err)
	}

	// If a secret is used as identity reference, we need to protect it until end of deletion too
	if identity.IsSecretIdentity(vsphereCluster) {
		secret, err := r.getIdentitySecret(ctx, vsphereCluster)
		if err != nil {
			return reconcile.Result{}, microerror.Mask(err)
		}

		err = r.addFinalizer(ctx, log, secret)
		if err != nil {
			return reconcile.Result{}, microerror.Mask(err)
		}
	}

	// Cleaner doesn't do anything for normal
	return ctrl.Result{}, nil
}

func (r *VSphereClusterReconciler) reconcileDelete(ctx context.Context, log logr.Logger, vsphereCluster *capv.VSphereCluster) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(vsphereCluster, key.CleanerFinalizerName) {
		// no-op in case the finalizer is not there (it could have been deleted manually)
		return ctrl.Result{}, nil
	}

	clusterName, ok := vsphereCluster.Labels[key.CapiClusterLabelKey]
	if !ok {
		log.V(1).Info("VSphereCluster doesn't have necessary label",
			"expectedLabelKey", key.CapiClusterLabelKey,
			"existingLabels", vsphereCluster.Labels)
		return ctrl.Result{}, nil
	}

	sess, err := r.getVCenterSession(ctx, vsphereCluster)
	if err != nil {
		return reconcile.Result{}, microerror.Mask(err)
	}

	log.V(1).Info("Cleaning Vsphere resources belonging to cluster", "cluster", clusterName)
	requeueForDeletion := false
	for _, c := range r.Cleaners {
		requeue, err := c.Clean(ctx, log, sess, vsphereCluster)
		if err != nil {
			return reconcile.Result{}, microerror.Mask(err)
		}
		requeueForDeletion = requeueForDeletion || requeue
	}

	if requeueForDeletion {
		log.V(1).Info("There is an ongoing clean-up process. Adding cluster into queue again")
		return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 10}, nil
	}

	log.Info("Clean-up is done. Removing finalizers")

	if identity.IsSecretIdentity(vsphereCluster) {
		secret, err := r.getIdentitySecret(ctx, vsphereCluster)
		if err != nil {
			return reconcile.Result{}, microerror.Mask(err)
		}

		err = r.removeFinalizer(ctx, log, secret)
		if err != nil {
			return reconcile.Result{}, microerror.Mask(err)
		}
	}

	err = r.removeFinalizer(ctx, log, vsphereCluster)

	return ctrl.Result{}, err
}

func (r *VSphereClusterReconciler) getVCenterSession(ctx context.Context, cluster *capv.VSphereCluster) (*session.Session, error) {
	params := session.NewParams().
		WithServer(cluster.Spec.Server).
		WithThumbprint(cluster.Spec.Thumbprint)

	creds, err := identity.GetCredentials(ctx, r.Client, cluster, cluster.Namespace)
	if err != nil {
		return nil, err
	}

	params = params.WithUserInfo(creds.Username, creds.Password)
	return session.GetOrCreate(ctx, params)
}

func (r *VSphereClusterReconciler) addFinalizer(ctx context.Context, log logr.Logger, obj client.Object) error {
	if controllerutil.ContainsFinalizer(obj, key.CleanerFinalizerName) {
		return nil
	}
	controllerutil.AddFinalizer(obj, key.CleanerFinalizerName)

	err := r.Update(ctx, obj)
	if err != nil {
		return fmt.Errorf("failed to patch object to add finalizer: %w", err)
	}

	log.Info("Added finalizer to object", "kind", obj.GetObjectKind().GroupVersionKind().Kind)

	return nil
}

func (r *VSphereClusterReconciler) removeFinalizer(ctx context.Context, log logr.Logger, obj client.Object) error {
	if !controllerutil.ContainsFinalizer(obj, key.CleanerFinalizerName) {
		return nil
	}
	controllerutil.RemoveFinalizer(obj, key.CleanerFinalizerName)
	err := r.Update(ctx, obj)
	if err != nil {
		return fmt.Errorf("failed to update object to remove finalizer: %w", err)
	}

	log.Info("Removed finalizer to object", "kind", obj.GetObjectKind().GroupVersionKind().Kind)

	return nil
}

func (r *VSphereClusterReconciler) getIdentitySecret(ctx context.Context, cluster *capv.VSphereCluster) (*v1.Secret, error) {
	secret := &v1.Secret{}

	err := r.Get(ctx, client.ObjectKey{
		Namespace: cluster.Namespace,
		Name:      cluster.Spec.IdentityRef.Name,
	}, secret)

	return secret, err
}
