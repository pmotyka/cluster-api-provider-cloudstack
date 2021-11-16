/*
Copyright 2021.

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

	"github.com/apache/cloudstack-go/v2/cloudstack"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/cluster-api/util"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "gitlab.aws.dev/ce-pike/merida/cluster-api-provider-capc/api/v1alpha4"
	"gitlab.aws.dev/ce-pike/merida/cluster-api-provider-capc/pkg/cloud"
)

// CloudStackClusterReconciler reconciles a CloudStackCluster object.
type CloudStackClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	CS     *cloudstack.CloudStackClient
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=cloudstackclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=cloudstackclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=cloudstackclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status,verbs=get;list;watch

// TODO review whether these unnamed groups are used and if so add clarity via a comment.
// +kubebuilder:rbac:groups="",resources=secrets;,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *CloudStackClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (retRes ctrl.Result, retErr error) {
	log := log.FromContext(ctx)

	// Get CloudStack cluster.
	csCluster := &infrav1.CloudStackCluster{}
	if retErr = r.Client.Get(ctx, req.NamespacedName, csCluster); retErr != nil {
		if client.IgnoreNotFound(retErr) == nil {
			log.Info("Cluster not found.")
		}
		return ctrl.Result{}, client.IgnoreNotFound(retErr)
	}

	// Get CAPI cluster.
	cluster, retErr := util.GetOwnerCluster(ctx, r.Client, csCluster.ObjectMeta)
	if retErr != nil {
		return reconcile.Result{}, retErr
	} else if cluster == nil {
		log.Info("Waiting for CAPI Cluster controller to set owner reference on CloudStack cluster.")
		return reconcile.Result{}, nil
	}

	// List VMs.
	vmInstances, retErr := r.clusterVMInstances(ctx, cluster, csCluster)
	if retErr != nil {
		return ctrl.Result{}, retErr
	}
	log.Info("Cluster instances", "instances", vmInstances)

	// Setup patcher. This ensures modifications to the csCluster copy fetched above are patched into the origin.
	if patchHelper, retErr := patch.NewHelper(csCluster, r.Client); retErr != nil {
		return ctrl.Result{}, retErr
	} else {
		defer func() {
			if err := patchHelper.Patch(ctx, csCluster); retErr == nil && err != nil {
				retErr = err
			}
		}()
	}

	// Delete Cluster Resources if deletion timestamp present.
	if !csCluster.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(log, csCluster)
	}

	// Reconcile remaining clusters.
	return r.reconcile(log, csCluster)
}

// Actually reconcile cluster.
func (r *CloudStackClusterReconciler) reconcile(
	log logr.Logger,
	csCluster *infrav1.CloudStackCluster) (ctrl.Result, error) {

	// Prevent premature deletion of the csCluster construct from CAPI.
	controllerutil.AddFinalizer(csCluster, infrav1.ClusterFinalizer)

	// Fetch cluster info -- sets cluster to ready if no errors in fetch.
	err := cloud.FetchClusterInfo(r.CS, csCluster)
	if err == nil {
		log.Info("Fetched cluster info successfully.", "clusterSpec", csCluster.Spec, "clusterStatus", csCluster.Status)
	}
	return ctrl.Result{}, err
}

// Delete a cluster.
func (r *CloudStackClusterReconciler) reconcileDelete(
	log logr.Logger,
	csCluster *infrav1.CloudStackCluster) (retRes ctrl.Result, retErr error) {

	log.Info("Deleting cluster...")

	defer func() { // Don't remove finalizer if Cluster destroy returned an error.
		if retErr == nil {
			controllerutil.RemoveFinalizer(csCluster, infrav1.ClusterFinalizer)
		}
	}()

	// TODO Decide what resources to remove w/Cluster if any.
	// cloud.DestroyCluster(r.CS, csStackCluster)

	return ctrl.Result{}, nil
}

// Called in main, this registers the cluster reconciler to the CAPI controller manager.
func (r *CloudStackClusterReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&infrav1.CloudStackCluster{}).Complete(r)
}

func (r *CloudStackClusterReconciler) clusterVMInstances(
	ctx context.Context,
	cluster *clusterv1.Cluster,
	csCluster *infrav1.CloudStackCluster) ([]*infrav1.CloudStackMachine, error) {
	labels := map[string]string{clusterv1.ClusterLabelName: cluster.Name}
	vmInstances := &infrav1.CloudStackMachineList{}
	if err := r.Client.List(
		ctx, vmInstances,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(labels)); err != nil {
		return nil, err
	}
	machines := make([]*infrav1.CloudStackMachine, len(vmInstances.Items))
	for i := range vmInstances.Items {
		machines[i] = &vmInstances.Items[i]
	}

	return machines, nil
}