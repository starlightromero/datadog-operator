// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package datadogagent

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"testing"

	apicommon "github.com/DataDog/datadog-operator/apis/datadoghq/common"
	datadoghqv1alpha1 "github.com/DataDog/datadog-operator/apis/datadoghq/v1alpha1"
	"github.com/DataDog/datadog-operator/apis/datadoghq/v1alpha1/test"
	datadoghqv2alpha1 "github.com/DataDog/datadog-operator/apis/datadoghq/v2alpha1"
	"github.com/DataDog/datadog-operator/controllers/datadogagent/object"
	"github.com/DataDog/datadog-operator/pkg/kubernetes"
	edsdatadoghqv1alpha1 "github.com/DataDog/extendeddaemonset/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1beta1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/diff"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// TODO: for now, we're just checking the agent DaemonSet, but we should expand
// this with other resources: DCA deployment, cluster roles, etc.
type reconciledResources struct {
	agentDaemonSet *appsv1.DaemonSet
}

// The input for these tests is a datadoghqv1alpha1.DatadogAgent and an
// equivalent datadoghqv2alpha1.DatadogAgent.
// The tests reconcile the V1 DatadogAgent using the reconciler V1, and the V2
// DatadogAgent using the reconciler V2.
// The tests check that the reconciled resources (Agent DaemonSet, etc.) are the
// same in both cases.
func TestCompareReconcilerV1WithReconcilerV2(t *testing.T) {
	datadogAgentName := "foo"
	datadogAgentNamespace := "bar"
	apiKey := "0000000000000000000000"
	appKey := "0000000000000000000000"

	logf.SetLogger(zap.New(zap.UseDevMode(true)))

	tests := []struct {
		name  string
		ddaV1 *datadoghqv1alpha1.DatadogAgent
		ddaV2 *datadoghqv2alpha1.DatadogAgent
	}{
		{
			name: "compare V1 and V2 basic deployments",
			ddaV1: test.NewDefaultedDatadogAgent(
				datadogAgentNamespace,
				datadogAgentName,
				&test.NewDatadogAgentOptions{
					OrchestratorExplorerDisabled: true,
				},
			),
			ddaV2: &datadoghqv2alpha1.DatadogAgent{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:  datadogAgentNamespace,
					Name:       datadogAgentName,
					Labels:     map[string]string{},
					Finalizers: []string{"finalizer.agent.datadoghq.com"},
				},
				Spec: datadoghqv2alpha1.DatadogAgentSpec{
					Global: &datadoghqv2alpha1.GlobalConfig{
						Credentials: &datadoghqv2alpha1.DatadogCredentials{
							APIKey: &apiKey,
							AppKey: &appKey,
						},
					},
				},
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			reconciledResourcesV1, err := reconcileWithV1(testCase.ddaV1)
			assert.NoError(t, err)
			daemonSetSpecV1 := reconciledResourcesV1.agentDaemonSet.Spec

			reconciledResourcesV2, err := reconcileWithV2(testCase.ddaV1, testCase.ddaV2)
			assert.NoError(t, err)
			daemonSetSpecV2 := reconciledResourcesV2.agentDaemonSet.Spec

			// TODO: There are some differences between the reconciled DaemonSets.
			// Some of the differences might be OK if we decided to changed the behavior in V2.

			// TODO: Different match labels
			daemonSetSpecV2.Selector.MatchLabels[kubernetes.AppKubernetesInstanceLabelKey] = "agent"
			daemonSetSpecV2.Selector.MatchLabels[kubernetes.AppKubernetesManageByLabelKey] = "datadog-operator"
			daemonSetSpecV2.Selector.MatchLabels[kubernetes.AppKubernetesNameLabelKey] = "datadog-agent-deployment"
			daemonSetSpecV2.Selector.MatchLabels[kubernetes.AppKubernetesPartOfLabelKey] = "bar-foo"
			daemonSetSpecV2.Selector.MatchLabels[kubernetes.AppKubernetesVersionLabelKey] = ""

			// TODO: Different template label
			daemonSetSpecV2.Template.Labels[kubernetes.AppKubernetesInstanceLabelKey] = "agent"

			// TODO: Missing fields in template
			daemonSetSpecV2.Template.Namespace = "bar"
			daemonSetSpecV2.Template.GenerateName = "foo"

			// TODO: daemonSetSpecV2 missing 2 volumes: "checksd", "runtimesocketdir"
			// TODO: daemonSetSpecV2 has a different path set in the "dsdsocket" volume.
			checksdVol := corev1.Volume{}
			runtimeSocketDirVol := corev1.Volume{}
			dsdsocketVol := corev1.Volume{}
			for _, vol := range daemonSetSpecV1.Template.Spec.Volumes {
				if vol.Name == "checksd" {
					v := vol
					checksdVol = v
				} else if vol.Name == "runtimesocketdir" {
					v := vol
					runtimeSocketDirVol = v
				} else if vol.Name == "dsdsocket" {
					v := vol
					dsdsocketVol = v
				}
			}

			// TODO: daemonSetSpecV2 has a path set in the "dsdsocket" volume.
			idxDsdSocketVol := 0
			found := false
			for i, vol := range daemonSetSpecV2.Template.Spec.Volumes {
				if vol.Name == "dsdsocket" {
					idxDsdSocketVol = i
					found = true
					break
				}
			}
			if found {
				daemonSetSpecV2.Template.Spec.Volumes[idxDsdSocketVol] = dsdsocketVol
			}

			daemonSetSpecV2.Template.Spec.Volumes = append(daemonSetSpecV2.Template.Spec.Volumes, checksdVol)
			daemonSetSpecV2.Template.Spec.Volumes = append(daemonSetSpecV2.Template.Spec.Volumes, runtimeSocketDirVol)
			sort.Slice(daemonSetSpecV1.Template.Spec.Volumes, func(i, j int) bool {
				return daemonSetSpecV1.Template.Spec.Volumes[i].Name < daemonSetSpecV1.Template.Spec.Volumes[j].Name
			})
			sort.Slice(daemonSetSpecV2.Template.Spec.Volumes, func(i, j int) bool {
				return daemonSetSpecV2.Template.Spec.Volumes[i].Name < daemonSetSpecV2.Template.Spec.Volumes[j].Name
			})

			// TODO: missing "init-config" container ENVs:
			// DD_COLLECT_KUBERNETES_EVENTS=false
			// DD_LEADER_LEASE_NAME=foo-leader-election
			// DD_LOGS_ENABLED=false
			// DD_LOGS_CONFIG_CONTAINER_COLLECT_ALL=false
			// DD_LOGS_CONFIG_K8S_CONTAINER_USE_FILE=false
			// DD_LOG_LEVEL=info
			// DD_API_KEY=SECRET_KEY_REF
			// DD_DOGSTATSD_ORIGIN_DETECTION=false
			// DD_DOGSTATSD_SOCKET=/var/run/datadog/statsd/statsd.sock

			// TODO: "init-config" container ENVs with different values:
			// DD_LEADER_ELECTION=false in v1

			// TODO: extra ENVs set in V2
			// DD_CLUSTER_AGENT_ENABLED=true
			// DD_CLUSTER_AGENT_KUBERNETES_SERVICE_NAME=foo-cluster-agent
			// DD_CLUSTER_AGENT_TOKEN_NAME=foo-token

			// simplify for now
			daemonSetSpecV2.Template.Spec.InitContainers[1].Env = daemonSetSpecV1.Template.Spec.InitContainers[1].Env

			// TODO: daemonSetSpecv2 "init-config" container missing 3 volumes: "checksd", "config", "runtimesocketdir"
			// Simplify for now
			daemonSetSpecV2.Template.Spec.InitContainers[1].VolumeMounts = daemonSetSpecV1.Template.Spec.InitContainers[1].VolumeMounts

			// TODO: v2 envs in "agent" container are different
			daemonSetSpecV2.Template.Spec.Containers[0].Env = daemonSetSpecV1.Template.Spec.Containers[0].Env

			// TODO: daemonSetSpecv2 "agent" container missing 2 volumes: "checksd", "runtimesocketdir"
			daemonSetSpecV2.Template.Spec.Containers[0].VolumeMounts = daemonSetSpecV1.Template.Spec.Containers[0].VolumeMounts

			// TODO: v1 sets imagePullPolicy to "IfNotPresent". In v2 is "".
			daemonSetSpecV2.Template.Spec.InitContainers[0].ImagePullPolicy = "IfNotPresent"
			daemonSetSpecV2.Template.Spec.InitContainers[1].ImagePullPolicy = "IfNotPresent"
			daemonSetSpecV2.Template.Spec.Containers[0].ImagePullPolicy = "IfNotPresent"

			// TODO: v2 doesn't set update strategy
			daemonSetSpecV2.UpdateStrategy = daemonSetSpecV1.UpdateStrategy

			assert.Equal(
				t,
				daemonSetSpecV1,
				daemonSetSpecV2,
				fmt.Sprintf(
					"The agent DaemonSets reconciled are not equal:\n %s",
					diff.ObjectGoPrintSideBySide(daemonSetSpecV1, daemonSetSpecV2),
				),
			)
		})
	}
}

func reconcileWithV1(dda *datadoghqv1alpha1.DatadogAgent) (reconciledResources, error) {
	k8sClient := fake.NewFakeClient()
	reconciler := newReconciler(false, k8sClient)

	err := k8sClient.Create(context.TODO(), dda)
	if err != nil {
		return reconciledResources{}, err
	}

	createRequiredResources(k8sClient, dda)

	_, err = reconciler.Reconcile(context.TODO(), newRequest(dda.Namespace, dda.Name))
	if err != nil {
		return reconciledResources{}, err
	}

	ds := &appsv1.DaemonSet{}
	daemonSetName := fmt.Sprintf("%s-agent", dda.Name)
	err = k8sClient.Get(context.TODO(), types.NamespacedName{Namespace: dda.Namespace, Name: daemonSetName}, ds)
	if err != nil {
		return reconciledResources{}, err
	}

	return reconciledResources{agentDaemonSet: ds}, nil
}

func reconcileWithV2(ddaV1 *datadoghqv1alpha1.DatadogAgent, ddaV2 *datadoghqv2alpha1.DatadogAgent) (reconciledResources, error) {
	k8sClient := fake.NewFakeClient()
	reconciler := newReconciler(true, k8sClient)

	err := k8sClient.Create(context.TODO(), ddaV2)
	if err != nil {
		return reconciledResources{}, err
	}

	createRequiredResources(k8sClient, ddaV1)

	_, err = reconciler.Reconcile(context.TODO(), newRequest(ddaV2.Namespace, ddaV2.Name))
	if err != nil {
		return reconciledResources{}, err
	}

	ds := &appsv1.DaemonSet{}
	daemonSetName := fmt.Sprintf("%s-agent", ddaV2.Name)
	err = k8sClient.Get(context.TODO(), types.NamespacedName{Namespace: ddaV2.Namespace, Name: daemonSetName}, ds)
	if err != nil {
		return reconciledResources{}, err
	}

	return reconciledResources{agentDaemonSet: ds}, nil
}

func newReconciler(useV2 bool, client client.Client) Reconciler {
	eventBroadcaster := record.NewBroadcaster()
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "TestReconcileDatadogAgent_Reconcile"})
	forwarders := dummyManager{}

	nameForLogs := "reconciler-v1"
	if useV2 {
		nameForLogs = "reconciler-v2"
	}

	return Reconciler{
		client:     client,
		scheme:     testScheme(),
		recorder:   recorder,
		log:        logf.Log.WithName(nameForLogs),
		forwarders: forwarders,
		options: ReconcilerOptions{
			V2Enabled: useV2,
		},
	}
}

func testScheme() *runtime.Scheme {
	s := scheme.Scheme

	s.AddKnownTypes(datadoghqv1alpha1.GroupVersion, &datadoghqv1alpha1.DatadogAgent{})
	s.AddKnownTypes(datadoghqv2alpha1.GroupVersion, &datadoghqv2alpha1.DatadogAgent{})
	s.AddKnownTypes(edsdatadoghqv1alpha1.GroupVersion, &edsdatadoghqv1alpha1.ExtendedDaemonSet{})
	s.AddKnownTypes(appsv1.SchemeGroupVersion, &appsv1.DaemonSet{})
	s.AddKnownTypes(appsv1.SchemeGroupVersion, &appsv1.Deployment{})
	s.AddKnownTypes(corev1.SchemeGroupVersion, &corev1.Secret{})
	s.AddKnownTypes(corev1.SchemeGroupVersion, &corev1.ServiceAccount{})
	s.AddKnownTypes(corev1.SchemeGroupVersion, &corev1.ConfigMap{})
	s.AddKnownTypes(rbacv1.SchemeGroupVersion, &rbacv1.ClusterRoleBinding{})
	s.AddKnownTypes(rbacv1.SchemeGroupVersion, &rbacv1.ClusterRole{})
	s.AddKnownTypes(rbacv1.SchemeGroupVersion, &rbacv1.Role{})
	s.AddKnownTypes(rbacv1.SchemeGroupVersion, &rbacv1.RoleBinding{})
	s.AddKnownTypes(policyv1.SchemeGroupVersion, &policyv1.PodDisruptionBudget{})
	s.AddKnownTypes(apiregistrationv1.SchemeGroupVersion, &apiregistrationv1.APIServiceList{})
	s.AddKnownTypes(apiregistrationv1.SchemeGroupVersion, &apiregistrationv1.APIService{})
	s.AddKnownTypes(networkingv1.SchemeGroupVersion, &networkingv1.NetworkPolicy{})

	return s
}

func createRequiredResources(client client.Client, dda *datadoghqv1alpha1.DatadogAgent) {
	labels := object.GetDefaultLabels(dda, apicommon.DefaultAgentResourceSuffix, getAgentVersion(dda))

	_ = client.Create(
		context.TODO(),
		test.NewSecret(
			dda.ObjectMeta.Namespace,
			"foo",
			&test.NewSecretOptions{
				Labels: labels,
				Data: map[string][]byte{
					"api-key": []byte(base64.StdEncoding.EncodeToString([]byte("api-foo"))),
					"app-key": []byte(base64.StdEncoding.EncodeToString([]byte("app-foo"))),
					"token":   []byte(base64.StdEncoding.EncodeToString([]byte("token-foo"))),
				},
			},
		),
	)

	installinfoCM, _ := buildInstallInfoConfigMap(dda)
	_ = client.Create(context.TODO(), installinfoCM)

	_ = client.Create(context.TODO(), buildServiceAccount(dda, getAgentServiceAccount(dda), ""))
}
