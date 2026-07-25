package statefulset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/ome/pkg/apis/ome/v1beta1"
	"sigs.k8s.io/ome/pkg/constants"
)

func TestCreateRawStatefulSet(t *testing.T) {
	testContainer := corev1.Container{
		Name:  "test-container",
		Image: "test-image:latest",
	}

	podSpec := &corev1.PodSpec{
		Containers: []corev1.Container{testContainer},
	}

	minReplicas := 3

	tests := []struct {
		name             string
		componentMeta    metav1.ObjectMeta
		componentExt     *v1beta1.ComponentExtensionSpec
		serviceName      string
		expectReplicas   *int32
		expectNilReplica bool
	}{
		{
			name: "statefulset with min replicas",
			componentMeta: metav1.ObjectMeta{
				Name:      "test-isvc",
				Namespace: "default",
				Labels: map[string]string{
					"app": "test-isvc",
				},
				Annotations: map[string]string{
					constants.PerPodDNS: "true",
				},
			},
			componentExt: &v1beta1.ComponentExtensionSpec{
				MinReplicas: &minReplicas,
			},
			serviceName: "test-isvc",
		},
		{
			name: "statefulset with nil replicas",
			componentMeta: metav1.ObjectMeta{
				Name:      "test-isvc-noreplicas",
				Namespace: "default",
				Labels: map[string]string{
					"app": "test-isvc-noreplicas",
				},
			},
			componentExt:     &v1beta1.ComponentExtensionSpec{},
			serviceName:      "test-isvc-noreplicas",
			expectNilReplica: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sts := createRawStatefulSet(tt.componentMeta, tt.componentExt, podSpec.DeepCopy(), tt.serviceName)

			// ServiceName is the headless service name
			assert.Equal(t, tt.serviceName, sts.Spec.ServiceName)

			// Parallel pod management for per-pod-DNS engines
			assert.Equal(t, appsv1.ParallelPodManagement, sts.Spec.PodManagementPolicy)

			// Selector and pod template label match the truncated component name
			expectedApp := constants.TruncateNameWithMaxLength(tt.componentMeta.Name, 63)
			assert.Equal(t, expectedApp, sts.Spec.Selector.MatchLabels["app"])
			assert.Equal(t, expectedApp, sts.Spec.Template.Labels["app"])

			// ObjectMeta carried through
			assert.Equal(t, tt.componentMeta.Name, sts.Name)
			assert.Equal(t, tt.componentMeta.Namespace, sts.Namespace)

			// Replicas derived from MinReplicas (nil when unset)
			if tt.expectNilReplica {
				assert.Nil(t, sts.Spec.Replicas)
			} else {
				assert.NotNil(t, sts.Spec.Replicas)
				assert.Equal(t, int32(minReplicas), *sts.Spec.Replicas)
			}

			// Defaults applied
			assert.Equal(t, appsv1.RollingUpdateStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
			assert.NotNil(t, sts.Spec.RevisionHistoryLimit)
			assert.Equal(t, int32(10), *sts.Spec.RevisionHistoryLimit)

			// Pod spec defaulting reused from the deployment package
			assert.Equal(t, corev1.DNSClusterFirst, sts.Spec.Template.Spec.DNSPolicy)
			assert.Equal(t, corev1.RestartPolicyAlways, sts.Spec.Template.Spec.RestartPolicy)
		})
	}
}

func TestSetDefaultStatefulSetSpec(t *testing.T) {
	tests := []struct {
		name         string
		spec         *appsv1.StatefulSetSpec
		expectedType appsv1.StatefulSetUpdateStrategyType
	}{
		{
			name:         "empty spec gets defaults",
			spec:         &appsv1.StatefulSetSpec{},
			expectedType: appsv1.RollingUpdateStatefulSetStrategyType,
		},
		{
			name: "existing strategy preserved",
			spec: &appsv1.StatefulSetSpec{
				UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
					Type: appsv1.OnDeleteStatefulSetStrategyType,
				},
			},
			expectedType: appsv1.OnDeleteStatefulSetStrategyType,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setDefaultStatefulSetSpec(tt.spec)
			assert.Equal(t, tt.expectedType, tt.spec.UpdateStrategy.Type)
			assert.NotNil(t, tt.spec.RevisionHistoryLimit)
			assert.Equal(t, int32(10), *tt.spec.RevisionHistoryLimit)
		})
	}
}
