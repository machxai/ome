package raw

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	knapis "knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/ome/pkg/apis/ome/v1beta1"
	"sigs.k8s.io/ome/pkg/constants"
	"sigs.k8s.io/ome/pkg/controller/v1beta1/controllerconfig"
	"sigs.k8s.io/ome/pkg/controller/v1beta1/inferenceservice/reconcilers/autoscaler"
	"sigs.k8s.io/ome/pkg/controller/v1beta1/inferenceservice/reconcilers/deployment"
	"sigs.k8s.io/ome/pkg/controller/v1beta1/inferenceservice/reconcilers/ingress/services"
	"sigs.k8s.io/ome/pkg/controller/v1beta1/inferenceservice/reconcilers/pdb"
	"sigs.k8s.io/ome/pkg/controller/v1beta1/inferenceservice/reconcilers/service"
	"sigs.k8s.io/ome/pkg/controller/v1beta1/inferenceservice/reconcilers/statefulset"
)

// RawKubeReconciler reconciles the Native K8S Resources
type RawKubeReconciler struct {
	client              client.Client
	scheme              *runtime.Scheme
	Deployment          *deployment.DeploymentReconciler
	StatefulSet         *statefulset.StatefulSetReconciler
	Service             *service.ServiceReconciler
	Scaler              *autoscaler.AutoscalerReconciler
	PodDisruptionBudget *pdb.PDBReconciler
	URL                 *knapis.URL
}

// NewRawKubeReconciler creates raw kubernetes resource reconciler.
func NewRawKubeReconciler(client client.Client,
	clientset kubernetes.Interface,
	scheme *runtime.Scheme,
	componentMeta metav1.ObjectMeta,
	componentExt *v1beta1.ComponentExtensionSpec,
	kedaConfig *v1beta1.KedaConfig,
	podSpec *corev1.PodSpec,
) (*RawKubeReconciler, error) {
	as, err := autoscaler.NewAutoscalerReconciler(client, clientset, scheme, componentMeta, componentExt, kedaConfig)
	if err != nil {
		return nil, err
	}

	pdb := pdb.NewPDBReconciler(client, scheme, componentMeta, componentExt)
	url, err := createRawURL(clientset, componentMeta)
	if err != nil {
		return nil, err
	}

	// When per-pod-DNS mode is requested on an engine component, render a StatefulSet +
	// headless Service (the Service reconciler auto-detects the headless mode via the same
	// annotation) instead of a Deployment + ClusterIP Service so each replica gets a stable
	// per-pod DNS name. In this mode replicas are fixed, so no HPA/Scaler is used.
	perPodDNS := componentMeta.Annotations[constants.PerPodDNS] == "true"

	r := &RawKubeReconciler{
		client:              client,
		scheme:              scheme,
		Service:             service.NewServiceReconciler(client, scheme, componentMeta, componentExt, podSpec, nil),
		Scaler:              as,
		PodDisruptionBudget: pdb,
		URL:                 url,
	}

	if perPodDNS {
		r.StatefulSet = statefulset.NewStatefulSetReconciler(client, scheme, componentMeta, componentExt, podSpec, componentMeta.Name)
	} else {
		r.Deployment = deployment.NewDeploymentReconciler(client, scheme, componentMeta, componentExt, podSpec)
	}

	return r, nil
}

func createRawURL(clientset kubernetes.Interface, metadata metav1.ObjectMeta) (*knapis.URL, error) {
	ingressConfig, err := controllerconfig.NewIngressConfig(clientset)
	if err != nil {
		return nil, err
	}
	domainService := services.NewDomainService()
	url := &knapis.URL{}
	url.Scheme = "http"
	url.Host, err = domainService.GenerateDomainName(metadata.Name, metadata, ingressConfig)
	if err != nil {
		return nil, err
	}

	return url, nil
}

// Reconcile ...
func (r *RawKubeReconciler) Reconcile() (*appsv1.Deployment, error) {
	// In per-pod-DNS mode, reconcile a StatefulSet (with fixed replicas and no HPA/Scaler)
	// instead of a Deployment. The deployment return value is nil in this mode; status is
	// propagated from the StatefulSet by the common reconciler.
	if r.StatefulSet != nil {
		if _, err := r.StatefulSet.Reconcile(); err != nil {
			return nil, err
		}
		// reconcile Service (headless in this mode)
		if _, err := r.Service.Reconcile(); err != nil {
			return nil, err
		}
		// reconcile PDB
		if _, err := r.PodDisruptionBudget.Reconcile(); err != nil {
			return nil, err
		}
		// Skip the HPA/Scaler: per-pod-DNS engines have fixed replicas and the Scaler targets a Deployment.
		return nil, nil
	}

	// reconcile Deployments
	dply, err := r.Deployment.Reconcile()
	if err != nil {
		return nil, err
	}
	// reconcile Service
	_, err = r.Service.Reconcile()
	if err != nil {
		return nil, err
	}
	// reconcile HPA
	err = r.Scaler.Reconcile()
	if err != nil {
		return nil, err
	}
	// reconcile PDB
	_, err = r.PodDisruptionBudget.Reconcile()
	if err != nil {
		return nil, err
	}
	return dply, nil
}
