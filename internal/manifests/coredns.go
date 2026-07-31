package manifests

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"github.com/sivchari/rask/internal/cluster"
)

// CoreDNSImage is pinned to the latest coredns/coredns release verified
// present on registry.k8s.io during implementation.
const CoreDNSImage = "registry.k8s.io/coredns/coredns:v1.14.6"

const coreDNSNamespace = "kube-system"

var coreDNSLabels = map[string]string{
	"app.kubernetes.io/name":       "coredns",
	"app.kubernetes.io/managed-by": "rask",
	"k8s-app":                      "kube-dns",
}

// corefile is CoreDNS's standard "kubernetes plugin" configuration, the
// same shape kubeadm and most distributions ship.
const corefile = `.:53 {
    errors
    health {
       lameduck 5s
    }
    ready
    kubernetes ` + cluster.Domain + ` in-addr.arpa ip6.arpa {
       pods insecure
       fallthrough in-addr.arpa ip6.arpa
       ttl 30
    }
    prometheus :9153
    forward . /etc/resolv.conf {
       max_concurrent 1000
    }
    cache 30
    loop
    reload
    loadbalance
}
`

// ApplyCoreDNS applies CoreDNS's ServiceAccount, RBAC, ConfigMap,
// Deployment and Service (fixed at cluster.DNSServiceIP, which kubelet is
// already configured to use as every pod's resolv.conf nameserver) using
// clientset. The Deployment's container image is image (pass CoreDNSImage
// for rask's own default). Creating an object that already exists is not an
// error (see ApplyYAML's doc comment for why).
func ApplyCoreDNS(ctx context.Context, clientset kubernetes.Interface, image string) error {
	if err := ignoreAlreadyExists(clientset.CoreV1().ServiceAccounts(coreDNSNamespace).Create(ctx, coreDNSServiceAccount(), metav1.CreateOptions{})); err != nil {
		return fmt.Errorf("manifests: creating coredns ServiceAccount: %w", err)
	}

	if err := ignoreAlreadyExists(clientset.RbacV1().ClusterRoles().Create(ctx, coreDNSClusterRole(), metav1.CreateOptions{})); err != nil {
		return fmt.Errorf("manifests: creating coredns ClusterRole: %w", err)
	}

	if err := ignoreAlreadyExists(clientset.RbacV1().ClusterRoleBindings().Create(ctx, coreDNSClusterRoleBinding(), metav1.CreateOptions{})); err != nil {
		return fmt.Errorf("manifests: creating coredns ClusterRoleBinding: %w", err)
	}

	if err := ignoreAlreadyExists(clientset.CoreV1().ConfigMaps(coreDNSNamespace).Create(ctx, coreDNSConfigMap(), metav1.CreateOptions{})); err != nil {
		return fmt.Errorf("manifests: creating coredns ConfigMap: %w", err)
	}

	if err := ignoreAlreadyExists(clientset.AppsV1().Deployments(coreDNSNamespace).Create(ctx, coreDNSDeployment(image), metav1.CreateOptions{})); err != nil {
		return fmt.Errorf("manifests: creating coredns Deployment: %w", err)
	}

	if err := ignoreAlreadyExists(clientset.CoreV1().Services(coreDNSNamespace).Create(ctx, coreDNSService(), metav1.CreateOptions{})); err != nil {
		return fmt.Errorf("manifests: creating coredns Service: %w", err)
	}

	return nil
}

func ignoreAlreadyExists(_ any, err error) error {
	if apierrors.IsAlreadyExists(err) {
		return nil
	}

	return err
}

func coreDNSServiceAccount() *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: coreDNSNamespace, Labels: coreDNSLabels},
	}
}

func coreDNSClusterRole() *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "system:coredns", Labels: coreDNSLabels},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"endpoints", "services", "pods", "namespaces"},
				Verbs:     []string{"list", "watch"},
			},
			{
				APIGroups: []string{"discovery.k8s.io"},
				Resources: []string{"endpointslices"},
				Verbs:     []string{"list", "watch"},
			},
		},
	}
}

func coreDNSClusterRoleBinding() *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "system:coredns", Labels: coreDNSLabels},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     "system:coredns",
		},
		Subjects: []rbacv1.Subject{
			{Kind: rbacv1.ServiceAccountKind, Name: "coredns", Namespace: coreDNSNamespace},
		},
	}
}

func coreDNSConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: coreDNSNamespace, Labels: coreDNSLabels},
		Data:       map[string]string{"Corefile": corefile},
	}
}

func coreDNSDeployment(image string) *appsv1.Deployment {
	replicas := int32(1)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: coreDNSNamespace, Labels: coreDNSLabels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: coreDNSLabels},
				Spec: corev1.PodSpec{
					ServiceAccountName: "coredns",
					// The default dnsPolicy (ClusterFirst) makes kubelet
					// inject "nameserver <cluster.DNSServiceIP>" into the
					// pod's own /etc/resolv.conf — which for the CoreDNS
					// pod itself IS its own Service IP, a literal
					// self-loop independent of anything in the Corefile
					// (found via a real E2E run: CoreDNS CrashLoopBackOff
					// with "[FATAL] plugin/loop"). Default makes it use
					// the node's resolv.conf as-is, matching upstream
					// kubeadm/kops's CoreDNS manifests.
					DNSPolicy: corev1.DNSDefault,
					Containers: []corev1.Container{
						{
							Name:  "coredns",
							Image: image,
							Args:  []string{"-conf", "/etc/coredns/Corefile"},
							Ports: []corev1.ContainerPort{
								{Name: "dns", ContainerPort: 53, Protocol: corev1.ProtocolUDP},
								{Name: "dns-tcp", ContainerPort: 53, Protocol: corev1.ProtocolTCP},
								{Name: "metrics", ContainerPort: 9153, Protocol: corev1.ProtocolTCP},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "config-volume", MountPath: "/etc/coredns", ReadOnly: true},
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromInt32(8080)}},
								InitialDelaySeconds: 10,
								TimeoutSeconds:      5,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromInt32(8181)}},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config-volume",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: "coredns"},
								},
							},
						},
					},
				},
			},
		},
	}
}

func coreDNSService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-dns", Namespace: coreDNSNamespace, Labels: coreDNSLabels},
		Spec: corev1.ServiceSpec{
			ClusterIP: cluster.DNSServiceIP,
			Selector:  map[string]string{"k8s-app": "kube-dns"},
			Ports: []corev1.ServicePort{
				{Name: "dns", Port: 53, Protocol: corev1.ProtocolUDP, TargetPort: intstr.FromInt32(53)},
				{Name: "dns-tcp", Port: 53, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt32(53)},
				{Name: "metrics", Port: 9153, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt32(9153)},
			},
		},
	}
}
