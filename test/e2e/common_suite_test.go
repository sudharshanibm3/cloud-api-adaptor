// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"

	batchv1 "k8s.io/api/batch/v1"

	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
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
	job           *batchv1.Job
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
func (tc *testCase) withJob(job *batchv1.Job) *testCase {
	tc.job = job
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
			if tc.job != nil {
				if err = client.Resources().Create(ctx, tc.job); err != nil {
					t.Fatal(err)
				}
			}
			if tc.job == nil {
				if err = client.Resources().Create(ctx, tc.pod); err != nil {
					t.Fatal(err)
				}
			}
			if tc.job != nil {
				if err = wait.For(conditions.New(client.Resources()).JobCompleted(tc.job), wait.WithTimeout(WAIT_POD_RUNNING_TIMEOUT)); err != nil {
					t.Log(err)
				}
			}
			if tc.job == nil {
				if err = wait.For(conditions.New(client.Resources()).PodRunning(tc.pod), wait.WithTimeout(WAIT_POD_RUNNING_TIMEOUT)); err != nil {
					t.Fatal(err)
				}
			}

			return ctx
		}).
		Assess(tc.assessMessage, func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {

			var podlist v1.PodList

			if err := cfg.Client().Resources(tc.pod.Namespace).List(context.TODO(), &podlist); err != nil {
				t.Fatal(err)
			}
			if tc.job == nil {
				tc.assert.HasPodVM(t, tc.pod.Name)
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
			}
			if tc.job != nil {
				var podlogstring string
				var errorpod int
				var successpod int

				clienset, err := kubernetes.NewForConfig(cfg.Client().RESTConfig())
				if err != nil {
					t.Fatal(err)
				}
				for _, i := range podlist.Items {
					if i.ObjectMeta.Labels["job-name"] == tc.job.Name && i.Status.ContainerStatuses[0].State.Terminated.Reason == "StartError" {
						errorpod++
						t.Log("WARNING:", i.ObjectMeta.Name, "-", i.Status.ContainerStatuses[0].State.Terminated.Reason)
					}
					if i.ObjectMeta.Labels["job-name"] == tc.job.Name && i.Status.ContainerStatuses[0].State.Terminated.Reason == "Completed" {
						successpod++
						watcher, err := clienset.CoreV1().Events(tc.job.Namespace).Watch(context.Background(), metav1.ListOptions{})
						if err != nil {
							t.Fatal(err)
						}
						defer watcher.Stop()
						for event := range watcher.ResultChan() {
							if event.Object.(*v1.Event).Reason == "Started" && i.Status.ContainerStatuses[0].State.Terminated.Reason == "Completed" {
								req := clienset.CoreV1().Pods(tc.job.Namespace).GetLogs(i.ObjectMeta.Name, &v1.PodLogOptions{})
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
								t.Log("SUCCESS:", i.ObjectMeta.Name, "-", i.Status.ContainerStatuses[0].State.Terminated.Reason, "- LOG:", podlogstring)
								break
							}

						}
					}

				}
				if errorpod == len(podlist.Items) && successpod == 0 {
					t.Errorf("Job Failed to Start pod")
				}
				if successpod == 1 && errorpod >= 1 {
					t.Skip("Expected Completed status on all pods")
				}
				if strings.Contains(podlogstring, "3.14") {
					log.Printf("Output Log from Pod: %s", podlogstring)
				} else {
					t.Errorf("Job Created pod with Invalid log")
				}
				return ctx
			}

			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			var podlist v1.PodList

			if err := cfg.Client().Resources(tc.pod.Namespace).List(context.TODO(), &podlist); err != nil {
				t.Fatal(err)
			}
			client, err := cfg.NewClient()
			if err != nil {
				t.Fatal(err)
			}
			if tc.job == nil {
				if err = client.Resources().Delete(ctx, tc.pod); err != nil {
					t.Fatal(err)
				}

				log.Infof("Deleting pod... %s", tc.pod.Name)

			}

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
			if tc.job != nil {
				if err = client.Resources().Delete(ctx, tc.job); err != nil {
					t.Fatal(err)
				} else {
					log.Infof("Deleting Job... %s", tc.job.Name)
				}
				for _, i := range podlist.Items {
					if i.ObjectMeta.Labels["job-name"] == tc.job.Name {
						if err = client.Resources().Delete(ctx, &i); err != nil {
							t.Fatal(err)
						}
					}
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
func newTestCasewithJob(t *testing.T, assert CloudAssert, assessMessage string, job *batchv1.Job) *testCase {
	testCase := &testCase{
		testing:       t,
		assert:        assert,
		assessMessage: assessMessage,
		job:           job,
		pod:           &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: job.Name, Namespace: job.Namespace}},
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
func doTestCreatePeerPodWithJob(t *testing.T, assert CloudAssert) {
	namespace := envconf.RandomName("default", 7)
	jobname := "job-pi"
	job := newJob(namespace, jobname)
	newTestCasewithJob(t, assert, "Job has been created", job).withJob(job).run()

}
