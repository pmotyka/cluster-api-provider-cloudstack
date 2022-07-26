/*
Copyright 2022 The Kubernetes Authors.

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

package utils

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	infrav1 "sigs.k8s.io/cluster-api-provider-cloudstack/api/v1beta2"
	"sigs.k8s.io/cluster-api-provider-cloudstack/pkg/cloud"

	"github.com/pkg/errors"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type CloudClient interface {
	AsFailureDomainUser(*infrav1.CloudStackFailureDomainSpec) CloudStackReconcilerMethod
}

type CloudClientImplementation struct {
	*ReconciliationRunner
	CloudClient
}

func NewCloudClient(r *ReconciliationRunner) CloudClient {
	return &CloudClientImplementation{ReconciliationRunner: r}
}

// CreateFailureDomain creates a specified CloudStackFailureDomain CRD owned by the ReconcilationSubject.
func (r *ReconciliationRunner) CreateFailureDomain(fdSpec infrav1.CloudStackFailureDomainSpec) error {
	csFD := &infrav1.CloudStackFailureDomain{
		ObjectMeta: r.NewChildObjectMeta(fdSpec.Name),
		Spec:       fdSpec,
	}
	return errors.Wrap(r.K8sClient.Create(r.RequestCtx, csFD), "creating CloudStackFailureDomain")
}

// CreateFailureDomains creates a CloudStackFailureDomain CRD for each of the ReconcilationSubject's FailureDomains.
func (r *ReconciliationRunner) CreateFailureDomains(fdSpecs []infrav1.CloudStackFailureDomainSpec) CloudStackReconcilerMethod {
	return func() (ctrl.Result, error) {
		for _, fdSpec := range fdSpecs {
			if !strings.HasSuffix(fdSpec.Name, "-"+r.CAPICluster.Name) { // Add cluster name suffix if missing.
				fdSpec.Name = fdSpec.Name + "-" + r.CAPICluster.Name
			}
			if err := r.CreateFailureDomain(fdSpec); err != nil {
				if !strings.Contains(strings.ToLower(err.Error()), "already exists") {
					return reconcile.Result{}, errors.Wrap(err, "creating CloudStackFailureDomains")
				}
			}
		}
		return ctrl.Result{}, nil
	}
}

// GetFailureDomains gets CloudStackFailureDomains owned by a CloudStackCluster.
func (r *ReconciliationRunner) GetFailureDomains(fds *infrav1.CloudStackFailureDomainList) CloudStackReconcilerMethod {
	return func() (ctrl.Result, error) {
		capiClusterLabel := map[string]string{clusterv1.ClusterLabelName: r.CSCluster.GetLabels()[clusterv1.ClusterLabelName]}
		if err := r.K8sClient.List(
			r.RequestCtx,
			fds,
			client.InNamespace(r.Request.Namespace),
			client.MatchingLabels(capiClusterLabel),
		); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to list failure domains")
		}
		return ctrl.Result{}, nil
	}
}

// GetFailureDomainsAndRequeueIfMissing gets CloudStackFailureDomains owned by a CloudStackCluster and requeues if none are found.
func (r *ReconciliationRunner) GetFailureDomainsAndRequeueIfMissing(fds *infrav1.CloudStackFailureDomainList) CloudStackReconcilerMethod {
	return func() (ctrl.Result, error) {
		if res, err := r.GetFailureDomains(fds)(); r.ShouldReturn(res, err) {
			return res, err
		} else if len(fds.Items) < 1 {
			return r.RequeueWithMessage("no failure domains found, requeueing")
		}
		return ctrl.Result{}, nil
	}
}

// AsFailureDomainUser uses the credentials specified in the failure domain to set the ReconciliationSubject's CSUser client.
func (r *CloudClientImplementation) AsFailureDomainUser(fdSpec *infrav1.CloudStackFailureDomainSpec) CloudStackReconcilerMethod {
	return func() (ctrl.Result, error) {
		if !strings.HasSuffix(fdSpec.Name, "-"+r.CAPICluster.Name) { // Add cluster name suffix if missing.
			fdSpec.Name = fdSpec.Name + "-" + r.CAPICluster.Name
		}
		endpointCredentials := &corev1.Secret{}
		key := client.ObjectKey{Name: fdSpec.ACSEndpoint.Name, Namespace: fdSpec.ACSEndpoint.Namespace}
		if err := r.K8sClient.Get(r.RequestCtx, key, endpointCredentials); err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "getting ACSEndpoint secret with ref: %v", fdSpec.ACSEndpoint)
		}

		var err error
		if r.CSClient, err = cloud.NewClientFromK8sSecret(endpointCredentials); err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "parsing ACSEndpoint secret with ref: %v", fdSpec.ACSEndpoint)
		}

		// Transfer Cluster Domain & Account to FailureDomain as needed.
		if fdSpec.Account == "" {
			if r.CSCluster.Spec.Account != "" {
				fdSpec.Account = r.CSCluster.Spec.Account
				fdSpec.Domain = r.CSCluster.Spec.Domain
			}
		}

		if r.CSCluster.Spec.Account != "" { // Set r.CSUser CloudStack Client per Account and Domain.
			client, err := r.CSClient.NewClientInDomainAndAccount(fdSpec.Domain, fdSpec.Account)
			if err != nil {
				return ctrl.Result{}, err
			}
			r.CSUser = client
		} else { // Set r.CSUser CloudStack Client to r.CSClient since Account & Domain weren't provided.
			r.CSUser = r.CSClient
		}

		return ctrl.Result{}, nil
	}
}
