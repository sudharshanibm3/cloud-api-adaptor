package e2e

import (
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type podOption func(*corev1.Pod)

func withRestartPolicy(restartPolicy corev1.RestartPolicy) podOption {
	return func(p *corev1.Pod) {
		p.Spec.RestartPolicy = restartPolicy
	}
}

func withCommand(command []string) podOption {
	return func(p *corev1.Pod) {
		p.Spec.Containers[0].Command = command
	}
}

func withConfigMapBinding(mountPath string, configMapName string) podOption {
	return func(p *corev1.Pod) {
		p.Spec.Containers[0].VolumeMounts = append(p.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "config-volume", MountPath: mountPath})
		p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "config-volume", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configMapName}}}})
	}
}

func withSecretBinding(mountPath string, secretName string) podOption {
	return func(p *corev1.Pod) {
		p.Spec.Containers[0].VolumeMounts = append(p.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "secret-volume", MountPath: mountPath})
		p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "secret-volume", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secretName}}})
	}
}

func newPod(namespace string, podName string, containerName string, imageName string, options ...podOption) *corev1.Pod {
	runtimeClassName := "kata-remote"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: namespace},
		Spec: corev1.PodSpec{
			Containers:       []corev1.Container{{Name: containerName, Image: imageName}},
			RuntimeClassName: &runtimeClassName,
		},
	}

	for _, option := range options {
		option(pod)
	}

	return pod
}

func newNginxPod(namespace string) *corev1.Pod {
	return newPod(namespace, "nginx", "nginx", "nginx", withRestartPolicy(corev1.RestartPolicyNever))
}

func newBusyboxPod(namespace string) *corev1.Pod {
	return newPod(namespace, "busybox-pod", "busybox", "quay.io/prometheus/busybox:latest", withCommand([]string{"/bin/sh", "-c", "sleep 3600"}))
}

func newNginxPodWithConfigMap(namespace string, configMapName string) *corev1.Pod {
	return newPod(namespace, "nginx-configmap-pod", "nginx-configmap", "nginx", withRestartPolicy(corev1.RestartPolicyNever), withConfigMapBinding("/etc/config", configMapName))
}

func newNginxPodWithSecret(namespace string, secretName string) *corev1.Pod {
	return newPod(namespace, "nginx-secret-pod", "nginx-secret", "nginx", withRestartPolicy(corev1.RestartPolicyNever), withSecretBinding("/etc/secret", secretName))
}

// newConfigMap returns a new config map object.
func newConfigMap(namespace string, name string, configMapData map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       configMapData,
	}
}

// newSecret returns a new secret object.
func newSecret(namespace string, name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       data,
	}
}

func newBuilderPod(namespace string, name string, containerName string, runtimeclass string, configmapname string, clusterIP string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:         name,
				Image:        "gcr.io/kaniko-project/executor:latest",
				Args:         []string{"--custom-platform=linux/amd64", "--dockerfile=/build-context/Dockerfile", "--context=dir:///build-context", "--destination=" + clusterIP + ":5000/user-image:latest"},
				VolumeMounts: []corev1.VolumeMount{{Name: configmapname, MountPath: "/" + configmapname}},
				Env:          []corev1.EnvVar{{Name: "DOCKER_CONFIG", Value: "/kaniko/.docker/"}, {Name: "DOCKER_CONTENT_TRUST", Value: "1"}},
			}},
			Volumes: []corev1.Volume{{
				Name: configmapname,
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: configmapname},
					},
				},
			}},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}

func newUserPod(namespace string, name string, containerName string, runtimeclass string, clusterIP string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  containerName,
					Image: clusterIP + ":5000/user-image:latest",
				},
			},
			DNSPolicy:        "ClusterFirst",
			RestartPolicy:    "Never",
			RuntimeClassName: &runtimeclass,
		},
	}
}
func newDaemonSet(namespace string, name string, mountpath string, folder string, filename string, filecontent string) *appsv1.DaemonSet {
	var security bool = true
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"app": name}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:            name,
						Image:           "busybox",
						Command:         []string{"/bin/sh", "-c", fmt.Sprintf("rm -rf %s/172* && mkdir -p %s/%s && echo '%s' > %s/%s/%s && sleep 3600", mountpath, mountpath, folder, filecontent, mountpath, folder, filename)},
						SecurityContext: &corev1.SecurityContext{Privileged: &security},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "host-root",
							MountPath: "/host",
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "host-root",
						VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{Path: "/"},
						},
					}},
				},
			},
		},
	}
}

// CloudAssert defines assertions to perform on the cloud provider.
type CloudAssert interface {
	HasPodVM(t *testing.T, id string) // Assert there is a PodVM with `id`.
}
