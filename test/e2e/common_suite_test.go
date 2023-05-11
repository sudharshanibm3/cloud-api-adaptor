// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	envconf "sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

const WAIT_POD_RUNNING_TIMEOUT = time.Second * 300

// testCommand is a list of commands to execute inside the pod container,
// each with a function to test if the command outputs the value the test
// expects it to on the stdout stream
type testCommand struct {
	command             []string
	testCommandStdoutFn func(stdout bytes.Buffer) bool
	containerName       string
}

type testCase struct {
	testing       *testing.T
	assert        CloudAssert
	assessMessage string
	pod           *v1.Pod
	configMap     *v1.ConfigMap
	secret        *v1.Secret
	testCommands  []testCommand
}

func (tc *testCase) withConfigMap(configMap *v1.ConfigMap) *testCase {
	tc.configMap = configMap
	return tc
}

func (tc *testCase) withSecret(secret *v1.Secret) *testCase {
	tc.secret = secret
	return tc
}

func (tc *testCase) withTestCommands(testCommands []testCommand) *testCase {
	tc.testCommands = testCommands
	return tc
}

func (tc *testCase) run() {
	podFeature := features.New(fmt.Sprintf("%s Pod", tc.pod.Name)).
		WithSetup("Create pod", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client, err := cfg.NewClient()
			if err != nil {
				t.Fatal(err)
			}

			if tc.configMap != nil {
				if err = client.Resources().Create(ctx, tc.configMap); err != nil {
					t.Fatal(err)
				}
			}

			if tc.secret != nil {
				if err = client.Resources().Create(ctx, tc.secret); err != nil {
					t.Fatal(err)
				}
			}

			if err = client.Resources().Create(ctx, tc.pod); err != nil {
				t.Fatal(err)
			}

			if err = wait.For(conditions.New(client.Resources()).PodRunning(tc.pod), wait.WithTimeout(WAIT_POD_RUNNING_TIMEOUT)); err != nil {
				t.Fatal(err)
			}

			return ctx
		}).
		Assess(tc.assessMessage, func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			tc.assert.HasPodVM(t, tc.pod.Name)

			var podlist v1.PodList

			if err := cfg.Client().Resources(tc.pod.Namespace).List(context.TODO(), &podlist); err != nil {
				t.Fatal(err)
			}

			for _, testCommand := range tc.testCommands {
				var stdout, stderr bytes.Buffer

				for _, podItem := range podlist.Items {
					if podItem.ObjectMeta.Name == tc.pod.Name {
						if err := cfg.Client().Resources(tc.pod.Namespace).ExecInPod(ctx, tc.pod.Namespace, tc.pod.Name, testCommand.containerName, testCommand.command, &stdout, &stderr); err != nil {
							t.Log(stderr.String())
							t.Fatal(err)
						}

						if !testCommand.testCommandStdoutFn(stdout) {
							t.Fatal(fmt.Errorf("Command %v running in container %s produced unexpected output on stdout: %s", testCommand.command, testCommand.containerName, stdout.String()))
						}
					}
				}
			}

			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client, err := cfg.NewClient()
			if err != nil {
				t.Fatal(err)
			}

			if err = client.Resources().Delete(ctx, tc.pod); err != nil {
				t.Fatal(err)
			}

			log.Infof("Deleting pod... %s", tc.pod.Name)

			if tc.configMap != nil {
				if err = client.Resources().Delete(ctx, tc.configMap); err != nil {
					t.Fatal(err)
				}

				log.Infof("Deleting Configmap... %s", tc.configMap.Name)
			}

			if tc.secret != nil {
				if err = client.Resources().Delete(ctx, tc.secret); err != nil {
					t.Fatal(err)
				} else {
					log.Infof("Deleting Secret... %s", tc.secret.Name)
				}
			}

			return ctx
		}).Feature()
	testEnv.Test(tc.testing, podFeature)
}

func newTestCase(t *testing.T, assert CloudAssert, assessMessage string, pod *v1.Pod) *testCase {
	testCase := &testCase{
		testing:       t,
		assert:        assert,
		assessMessage: assessMessage,
		pod:           pod,
	}

	return testCase
}

// doTestCreateSimplePod tests a simple peer-pod can be created.
func doTestCreateSimplePod(t *testing.T, assert CloudAssert) {
	namespace := envconf.RandomName("default", 7)
	pod := newNginxPod(namespace)
	newTestCase(t, assert, "PodVM is created", pod).run()
}

func doTestCreatePodWithConfigMap(t *testing.T, assert CloudAssert) {
	namespace := envconf.RandomName("default", 7)
	configMapName := "nginx-config"
	configMapFileName := "example.txt"
	configMapPath := "/etc/config/" + configMapFileName
	configMapContents := "Hello, world"
	configMapData := map[string]string{configMapFileName: configMapContents}
	pod := newNginxPodWithConfigMap(namespace, configMapName)
	configMap := newConfigMap(namespace, configMapName, configMapData)
	testCommands := []testCommand{
		{
			command:       []string{"cat", configMapPath},
			containerName: pod.Spec.Containers[0].Name,
			testCommandStdoutFn: func(stdout bytes.Buffer) bool {
				if stdout.String() == configMapContents {
					log.Infof("Data Inside Configmap: %s", stdout.String())
					return true
				} else {
					log.Errorf("Configmap has invalid Data: %s", stdout.String())
					return false
				}
			},
		},
	}

	newTestCase(t, assert, "Configmap is created and contains data", pod).withConfigMap(configMap).withTestCommands(testCommands).run()
}

func doTestCreatePodWithSecret(t *testing.T, assert CloudAssert) {
	//doTestCreatePod(t, assert, "Secret is created and contains data", pod)
	namespace := envconf.RandomName("default", 7)
	secretName := "nginx-secret"
	podKubeSecretsDir := "/etc/secret/"
	usernameFileName := "username"
	username := "admin"
	usernamePath := podKubeSecretsDir + usernameFileName
	passwordFileName := "password"
	password := "password"
	passwordPath := podKubeSecretsDir + passwordFileName
	secretData := map[string][]byte{passwordFileName: []byte(password), usernameFileName: []byte(username)}
	pod := newNginxPodWithSecret(namespace, secretName)
	secret := newSecret(namespace, secretName, secretData)

	testCommands := []testCommand{
		{
			command:       []string{"cat", usernamePath},
			containerName: pod.Spec.Containers[0].Name,
			testCommandStdoutFn: func(stdout bytes.Buffer) bool {
				if stdout.String() == username {
					log.Infof("Username from secret inside pod: %s", stdout.String())
					return true
				} else {
					log.Errorf("Username value from secret inside pod unexpected. Expected %s, got %s", username, stdout.String())
					return false
				}
			},
		},
		{
			command:       []string{"cat", passwordPath},
			containerName: pod.Spec.Containers[0].Name,
			testCommandStdoutFn: func(stdout bytes.Buffer) bool {
				if stdout.String() == password {
					log.Infof("Password from secret inside pod: %s", stdout.String())
					return true
				} else {
					log.Errorf("Password value from secret inside pod unexpected. Expected %s, got %s", password, stdout.String())
					return false
				}
			},
		},
	}

	newTestCase(t, assert, "Secret has been created and contains data", pod).withSecret(secret).withTestCommands(testCommands).run()
}

func doTestCreatePeerPodContainerWithExternalIPAccess(t *testing.T, assert CloudAssert) {
	namespace := envconf.RandomName("default", 7)
	pod := newBusyboxPod(namespace)
	testCommands := []testCommand{
		{
			command:       []string{"ping", "-c", "1", "www.google.com"},
			containerName: pod.Spec.Containers[0].Name,
			testCommandStdoutFn: func(stdout bytes.Buffer) bool {
				if stdout.String() != "" {
					log.Infof("Output of ping command in busybox : %s", stdout.String())
					return true
				} else {
					log.Info("No output from ping command")
					return false
				}
			},
		},
	}

	newTestCase(t, assert, "Peer Pod Container Connected to External IP", pod).withTestCommands(testCommands).run()
}
func doTestCreatePodWithUser(t *testing.T, assert CloudAssert) {
	var pod, builderpod *v1.Pod
	var folderName, fileContent string
	namespace := envconf.RandomName("default", 7)
	name := "user-pod-" + strconv.Itoa(rand.Intn(1000))
	clusterIP := "localhost"
	builderpodname := "builder-pod"
	registryservicename := "registry-service"
	configmapname := "build-context"
	configmapData := map[string]string{"Dockerfile": "FROM --platform=\"linux/amd64\" alpine:latest AS user-image\nRUN addgroup -S othergroup && adduser -S otheruser -G othergroup\nUSER otheruser\nENTRYPOINT [ \"/bin/sh\", \"-c\", \"whoami\" ]"}
	configmap := newConfigMap(namespace, configmapname, configmapData)
	mountPath := "host/etc/containerd/certs.d"
	fileName := "hosts.toml"
	daemonset := newDaemonSet(namespace, "node-debugger", mountPath, folderName, fileName, fileContent)
	userPodFeature := features.New("User Peer Pod").
		WithSetup("Create pod", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			var endpointlist v1.EndpointsList
			client, err := cfg.NewClient()
			if err != nil {
				t.Fatal(err)
			}

			if err = client.Resources().Create(ctx, configmap); err != nil {
				t.Fatal(err)
			}

			if err := cfg.Client().Resources(namespace).List(context.TODO(), &endpointlist); err != nil {
				t.Fatal(err)
			}

			for _, i := range endpointlist.Items {
				if i.ObjectMeta.Name == registryservicename {
					for _, subset := range i.Subsets {
						for _, addr := range subset.Addresses {
							builderpod = newBuilderPod(namespace, builderpodname, "kaniko", "kata-remote", configmapname, addr.IP)
							pod = newUserPod(namespace, name, name, "kata-remote", addr.IP)
							clusterIP = addr.IP
							folderName = clusterIP + ":5000"
							fileContent = "server = \"http://" + clusterIP + ":5000\"\n[host.\"http://" + clusterIP + ":5000\"]\n\tskip_verify = true\n"
							daemonset = newDaemonSet(namespace, "node-debugger", mountPath, folderName, fileName, fileContent)
							if err = client.Resources().Create(ctx, daemonset); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
			}
			if clusterIP == "localhost" {
				t.Fatal("Service not created...")
			}

			if err = client.Resources().Create(ctx, builderpod); err != nil {
				t.Fatal(err)
			}
			if err = client.Resources().Create(ctx, pod); err != nil {
				t.Fatal(err)
			}
			if err = wait.For(conditions.New(client.Resources()).PodPhaseMatch(pod, v1.PodSucceeded), wait.WithTimeout(WAIT_POD_RUNNING_TIMEOUT)); err != nil {
				t.Fatal(err)
			}

			return ctx
		}).
		Assess("Peer Pod Contains User", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			var podlist v1.PodList
			var podlogstring string
			err := cfg.Client().Resources(namespace).List(context.TODO(), &podlist)
			if err != nil {
				t.Fatal(err)
			}
			clienset, err := kubernetes.NewForConfig(cfg.Client().RESTConfig())
			if err != nil {
				t.Fatal(err)
			}
			for _, i := range podlist.Items {
				if i.ObjectMeta.Name == name {
					req := clienset.CoreV1().Pods(namespace).GetLogs(name, &v1.PodLogOptions{})
					podLogs, err := req.Stream(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer podLogs.Close()
					buf := new(bytes.Buffer)
					_, err = io.Copy(buf, podLogs)
					if err != nil {
						t.Fatal(err)
					}
					podlogstring = strings.TrimSpace(buf.String())
				}
			}

			if podlogstring == "otheruser" {
				log.Infof("Current User: %s", podlogstring)
			} else {
				t.Errorf("Invalid User: %s", podlogstring)
			}

			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client, err := cfg.NewClient()
			if err != nil {
				t.Fatal(err)
			}
			if err = client.Resources().Delete(ctx, pod); err != nil {
				t.Fatal(err)
			}
			if err = client.Resources().Delete(ctx, builderpod); err != nil {
				t.Fatal(err)
			}
			if err = client.Resources().Delete(ctx, configmap); err != nil {
				t.Fatal(err)
			}
			if err = client.Resources().Delete(ctx, daemonset); err != nil {
				t.Fatal(err)
			}

			return ctx
		}).Feature()
	testEnv.Test(t, userPodFeature)
}
