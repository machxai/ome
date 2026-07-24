package statefulset

import (
	"context"

	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"knative.dev/pkg/kmp"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/ome/pkg/apis/ome/v1beta1"
	"sigs.k8s.io/ome/pkg/constants"
	"sigs.k8s.io/ome/pkg/controller/v1beta1/inferenceservice/reconcilers/deployment"
	"sigs.k8s.io/ome/pkg/controller/v1beta1/inferenceservice/utils"
)

var log = logf.Log.WithName("StatefulSetReconciler")

// StatefulSetReconciler reconciles raw Kubernetes StatefulSet resources. It is used by the
// per-pod-DNS RawDeployment mode so each replica gets a stable per-pod DNS name
// (<name>-<i>.<serviceName>.<ns>.svc.cluster.local) via a headless Service.
type StatefulSetReconciler struct {
	client       kclient.Client
	scheme       *runtime.Scheme
	StatefulSet  *appsv1.StatefulSet
	componentExt *v1beta1.ComponentExtensionSpec
}

func NewStatefulSetReconciler(client kclient.Client,
	scheme *runtime.Scheme,
	componentMeta metav1.ObjectMeta,
	componentExt *v1beta1.ComponentExtensionSpec,
	podSpec *corev1.PodSpec,
	serviceName string) *StatefulSetReconciler {
	return &StatefulSetReconciler{
		client:       client,
		scheme:       scheme,
		StatefulSet:  createRawStatefulSet(componentMeta, componentExt, podSpec, serviceName),
		componentExt: componentExt,
	}
}

func createRawStatefulSet(componentMeta metav1.ObjectMeta,
	componentExt *v1beta1.ComponentExtensionSpec,
	podSpec *corev1.PodSpec,
	serviceName string) *appsv1.StatefulSet {

	podMetadata := componentMeta
	podMetadata.Labels["app"] = constants.TruncateNameWithMaxLength(componentMeta.Name, 63)
	utils.SetPodLabelsFromAnnotations(&podMetadata)
	// Reuse the deployment package's pod-spec defaulting so both render paths stay identical.
	deployment.SetDefaultPodSpec(podSpec)

	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: componentMeta,
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": constants.TruncateNameWithMaxLength(componentMeta.Name, 63),
				},
			},
			ServiceName:         serviceName,
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: podMetadata,
				Spec:       *podSpec,
			},
		},
	}

	if componentExt != nil && componentExt.MinReplicas != nil {
		replicas := int32(*componentExt.MinReplicas)
		statefulSet.Spec.Replicas = &replicas
	}

	setDefaultStatefulSetSpec(&statefulSet.Spec)

	return statefulSet
}

func setDefaultStatefulSetSpec(spec *appsv1.StatefulSetSpec) {
	if spec.UpdateStrategy.Type == "" {
		spec.UpdateStrategy.Type = appsv1.RollingUpdateStatefulSetStrategyType
	}
	if spec.RevisionHistoryLimit == nil {
		revisionHistoryLimit := int32(10)
		spec.RevisionHistoryLimit = &revisionHistoryLimit
	}
}

func (r *StatefulSetReconciler) checkStatefulSetExist() (constants.CheckResultType, *appsv1.StatefulSet, error) {
	existingStatefulSet := &appsv1.StatefulSet{}
	err := r.client.Get(context.TODO(), types.NamespacedName{
		Namespace: r.StatefulSet.ObjectMeta.Namespace,
		Name:      r.StatefulSet.ObjectMeta.Name,
	}, existingStatefulSet)
	if err != nil {
		if apierr.IsNotFound(err) {
			return constants.CheckResultCreate, nil, nil
		}
		return constants.CheckResultUnknown, nil, err
	}

	// Carry the observed cluster status onto the desired object so status propagation
	// (which reads StatefulSet.Status.ReadyReplicas/Replicas) reflects reality on subsequent
	// reconcile loops. This does not affect the spec diff below.
	r.StatefulSet.Status = existingStatefulSet.Status

	// Ignore fields related to scaling; replicas are fixed by the caller in this mode.
	ignoreFields := cmpopts.IgnoreFields(appsv1.StatefulSetSpec{}, "Replicas")

	// Perform a dry-run update to populate default values
	if err := r.client.Update(context.TODO(), r.StatefulSet, kclient.DryRunAll); err != nil {
		log.Error(err, "Failed to perform dry-run update of statefulset", "namespace", r.StatefulSet.Namespace, "name", r.StatefulSet.Name)
		return constants.CheckResultUnknown, nil, err
	}

	if existingStatefulSet.Spec.Replicas != nil {
		r.StatefulSet.Spec.Replicas = existingStatefulSet.Spec.Replicas
		log.V(1).Info("Preserving existing replicas in target state", "namespace", r.StatefulSet.Namespace, "name", r.StatefulSet.Name, "replicas", *r.StatefulSet.Spec.Replicas)
	}

	diff, err := kmp.SafeDiff(r.StatefulSet.Spec, existingStatefulSet.Spec, ignoreFields)
	if err != nil {
		return constants.CheckResultUnknown, nil, err
	}
	if diff != "" {
		log.Info("StatefulSets differ", "namespace", r.StatefulSet.Namespace, "name", r.StatefulSet.Name, "diff", diff)
		return constants.CheckResultUpdate, existingStatefulSet, nil
	}
	return constants.CheckResultExisted, existingStatefulSet, nil
}

func (r *StatefulSetReconciler) Reconcile() (*appsv1.StatefulSet, error) {
	checkResult, statefulSet, err := r.checkStatefulSetExist()
	if err != nil {
		return nil, err
	}
	log.Info("Reconciling statefulset", "namespace", r.StatefulSet.Namespace, "name", r.StatefulSet.Name, "checkResult", checkResult.String())

	var opErr error
	switch checkResult {
	case constants.CheckResultCreate:
		opErr = r.client.Create(context.TODO(), r.StatefulSet)
	case constants.CheckResultUpdate:
		opErr = r.client.Update(context.TODO(), r.StatefulSet)
	default:
		return statefulSet, nil
	}

	if opErr != nil {
		log.Error(opErr, "Failed to reconcile statefulset", "namespace", r.StatefulSet.Namespace, "name", r.StatefulSet.Name)
		return nil, opErr
	}

	return r.StatefulSet, nil
}
