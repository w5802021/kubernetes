/*
Copyright 2014 The Kubernetes Authors.

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

package apimachinery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubernetes/test/e2e/framework"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
	imageutils "k8s.io/kubernetes/test/utils/image"
	admissionapi "k8s.io/pod-security-admission/api"

	"github.com/onsi/ginkgo/v2"
	"k8s.io/apimachinery/pkg/types"
)

func extinguish(f *framework.Framework, totalNS int, maxAllowedAfterDel int, maxSeconds int) {
	ginkgo.By("Creating testing namespaces")
	wg := &sync.WaitGroup{}
	wg.Add(totalNS)
	for n := 0; n < totalNS; n++ {
		go func(n int) {
			defer wg.Done()
			defer ginkgo.GinkgoRecover()
			ns := fmt.Sprintf("nslifetest-%v", n)
			_, err := f.CreateNamespace(ns, nil)
			framework.ExpectNoError(err, "failed to create namespace: %s", ns)
		}(n)
	}
	wg.Wait()

	//Wait 10 seconds, then SEND delete requests for all the namespaces.
	ginkgo.By("Waiting 10 seconds")
	time.Sleep(10 * time.Second)
	deleteFilter := []string{"nslifetest"}
	deleted, err := framework.DeleteNamespaces(f.ClientSet, deleteFilter, nil /* skipFilter */)
	framework.ExpectNoError(err, "failed to delete namespace(s) containing: %s", deleteFilter)
	framework.ExpectEqual(len(deleted), totalNS)

	ginkgo.By("Waiting for namespaces to vanish")
	//Now POLL until all namespaces have been eradicated.
	framework.ExpectNoError(wait.Poll(2*time.Second, time.Duration(maxSeconds)*time.Second,
		func() (bool, error) {
			var cnt = 0
			nsList, err := f.ClientSet.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
			if err != nil {
				return false, err
			}
			for _, item := range nsList.Items {
				if strings.Contains(item.Name, "nslifetest") {
					cnt++
				}
			}
			if cnt > maxAllowedAfterDel {
				framework.Logf("Remaining namespaces : %v", cnt)
				return false, nil
			}
			return true, nil
		}))
}

func ensurePodsAreRemovedWhenNamespaceIsDeleted(f *framework.Framework) {
	ginkgo.By("Creating a test namespace")
	namespaceName := "nsdeletetest"
	namespace, err := f.CreateNamespace(namespaceName, nil)
	framework.ExpectNoError(err, "failed to create namespace: %s", namespaceName)

	ginkgo.By("Waiting for a default service account to be provisioned in namespace")
	err = framework.WaitForDefaultServiceAccountInNamespace(f.ClientSet, namespace.Name)
	framework.ExpectNoError(err, "failure while waiting for a default service account to be provisioned in namespace: %s", namespace.Name)

	ginkgo.By("Creating a pod in the namespace")
	podName := "test-pod"
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "nginx",
					Image: imageutils.GetPauseImageName(),
				},
			},
		},
	}
	pod, err = f.ClientSet.CoreV1().Pods(namespace.Name).Create(context.TODO(), pod, metav1.CreateOptions{})
	framework.ExpectNoError(err, "failed to create pod %s in namespace: %s", podName, namespace.Name)

	ginkgo.By("Waiting for the pod to have running status")
	framework.ExpectNoError(e2epod.WaitForPodRunningInNamespace(f.ClientSet, pod))

	ginkgo.By("Deleting the namespace")
	err = f.ClientSet.CoreV1().Namespaces().Delete(context.TODO(), namespace.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err, "failed to delete namespace: %s", namespace.Name)

	ginkgo.By("Waiting for the namespace to be removed.")
	maxWaitSeconds := int64(60) + *pod.Spec.TerminationGracePeriodSeconds
	framework.ExpectNoError(wait.Poll(1*time.Second, time.Duration(maxWaitSeconds)*time.Second,
		func() (bool, error) {
			_, err = f.ClientSet.CoreV1().Namespaces().Get(context.TODO(), namespace.Name, metav1.GetOptions{})
			if err != nil && apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, nil
		}))

	ginkgo.By("Recreating the namespace")
	namespace, err = f.CreateNamespace(namespaceName, nil)
	framework.ExpectNoError(err, "failed to create namespace: %s", namespaceName)

	ginkgo.By("Verifying there are no pods in the namespace")
	_, err = f.ClientSet.CoreV1().Pods(namespace.Name).Get(context.TODO(), pod.Name, metav1.GetOptions{})
	framework.ExpectError(err, "failed to get pod %s in namespace: %s", pod.Name, namespace.Name)
}

func ensureServicesAreRemovedWhenNamespaceIsDeleted(f *framework.Framework) {
	var err error

	ginkgo.By("Creating a test namespace")
	namespaceName := "nsdeletetest"
	namespace, err := f.CreateNamespace(namespaceName, nil)
	framework.ExpectNoError(err, "failed to create namespace: %s", namespaceName)

	ginkgo.By("Waiting for a default service account to be provisioned in namespace")
	err = framework.WaitForDefaultServiceAccountInNamespace(f.ClientSet, namespace.Name)
	framework.ExpectNoError(err, "failure while waiting for a default service account to be provisioned in namespace: %s", namespace.Name)

	ginkgo.By("Creating a service in the namespace")
	serviceName := "test-service"
	labels := map[string]string{
		"foo": "bar",
		"baz": "blah",
	}
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: serviceName,
		},
		Spec: v1.ServiceSpec{
			Selector: labels,
			Ports: []v1.ServicePort{{
				Port:       80,
				TargetPort: intstr.FromInt(80),
			}},
		},
	}
	service, err = f.ClientSet.CoreV1().Services(namespace.Name).Create(context.TODO(), service, metav1.CreateOptions{})
	framework.ExpectNoError(err, "failed to create service %s in namespace %s", serviceName, namespace.Name)

	ginkgo.By("Deleting the namespace")
	err = f.ClientSet.CoreV1().Namespaces().Delete(context.TODO(), namespace.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err, "failed to delete namespace: %s", namespace.Name)

	ginkgo.By("Waiting for the namespace to be removed.")
	maxWaitSeconds := int64(60)
	framework.ExpectNoError(wait.Poll(1*time.Second, time.Duration(maxWaitSeconds)*time.Second,
		func() (bool, error) {
			_, err = f.ClientSet.CoreV1().Namespaces().Get(context.TODO(), namespace.Name, metav1.GetOptions{})
			if err != nil && apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, nil
		}))

	ginkgo.By("Recreating the namespace")
	namespace, err = f.CreateNamespace(namespaceName, nil)
	framework.ExpectNoError(err, "failed to create namespace: %s", namespaceName)

	ginkgo.By("Verifying there is no service in the namespace")
	_, err = f.ClientSet.CoreV1().Services(namespace.Name).Get(context.TODO(), service.Name, metav1.GetOptions{})
	framework.ExpectError(err, "failed to get service %s in namespace: %s", service.Name, namespace.Name)
}

// This test must run [Serial] due to the impact of running other parallel
// tests can have on its performance.  Each test that follows the common
// test framework follows this pattern:
//   1. Create a Namespace
//   2. Do work that generates content in that namespace
//   3. Delete a Namespace
// Creation of a Namespace is non-trivial since it requires waiting for a
// ServiceAccount to be generated.
// Deletion of a Namespace is non-trivial and performance intensive since
// its an orchestrated process.  The controller that handles deletion must
// query the namespace for all existing content, and then delete each piece
// of content in turn.  As the API surface grows to add more KIND objects
// that could exist in a Namespace, the number of calls that the namespace
// controller must orchestrate grows since it must LIST, DELETE (1x1) each
// KIND.
// There is work underway to improve this, but it's
// most likely not going to get significantly better until etcd v3.
// Going back to this test, this test generates 100 Namespace objects, and then
// rapidly deletes all of them.  This causes the NamespaceController to observe
// and attempt to process a large number of deletes concurrently.  In effect,
// it's like running 100 traditional e2e tests in parallel.  If the namespace
// controller orchestrating deletes is slowed down deleting another test's
// content then this test may fail.  Since the goal of this test is to soak
// Namespace creation, and soak Namespace deletion, its not appropriate to
// further soak the cluster with other parallel Namespace deletion activities
// that each have a variable amount of content in the associated Namespace.
// When run in [Serial] this test appears to delete Namespace objects at a
// rate of approximately 1 per second.
var _ = SIGDescribe("Namespaces [Serial]", func() {

	f := framework.NewDefaultFramework("namespaces")
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelBaseline

	/*
		Release: v1.11
		Testname: namespace-deletion-removes-pods
		Description: Ensure that if a namespace is deleted then all pods are removed from that namespace.
	*/
	framework.ConformanceIt("should ensure that all pods are removed when a namespace is deleted",
		func() { ensurePodsAreRemovedWhenNamespaceIsDeleted(f) })

	/*
		Release: v1.11
		Testname: namespace-deletion-removes-services
		Description: Ensure that if a namespace is deleted then all services are removed from that namespace.
	*/
	framework.ConformanceIt("should ensure that all services are removed when a namespace is deleted",
		func() { ensureServicesAreRemovedWhenNamespaceIsDeleted(f) })

	ginkgo.It("should delete fast enough (90 percent of 100 namespaces in 150 seconds)",
		func() { extinguish(f, 100, 10, 150) })

	// On hold until etcd3; see #7372
	ginkgo.It("should always delete fast (ALL of 100 namespaces in 150 seconds) [Feature:ComprehensiveNamespaceDraining]",
		func() { extinguish(f, 100, 0, 150) })

	/*
	   Release: v1.18
	   Testname: Namespace patching
	   Description: A Namespace is created.
	   The Namespace is patched.
	   The Namespace and MUST now include the new Label.
	*/
	framework.ConformanceIt("should patch a Namespace", func() {
		ginkgo.By("creating a Namespace")
		namespaceName := "nspatchtest-" + string(uuid.NewUUID())
		ns, err := f.CreateNamespace(namespaceName, nil)
		framework.ExpectNoError(err, "failed creating Namespace")
		namespaceName = ns.ObjectMeta.Name

		ginkgo.By("patching the Namespace")
		nspatch, err := json.Marshal(map[string]interface{}{
			"metadata": map[string]interface{}{
				"labels": map[string]string{"testLabel": "testValue"},
			},
		})
		framework.ExpectNoError(err, "failed to marshal JSON patch data")
		_, err = f.ClientSet.CoreV1().Namespaces().Patch(context.TODO(), namespaceName, types.StrategicMergePatchType, nspatch, metav1.PatchOptions{})
		framework.ExpectNoError(err, "failed to patch Namespace")

		ginkgo.By("get the Namespace and ensuring it has the label")
		namespace, err := f.ClientSet.CoreV1().Namespaces().Get(context.TODO(), namespaceName, metav1.GetOptions{})
		framework.ExpectNoError(err, "failed to get Namespace")
		framework.ExpectEqual(namespace.ObjectMeta.Labels["testLabel"], "testValue", "namespace not patched")
	})

})
