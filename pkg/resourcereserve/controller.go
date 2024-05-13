package resourcereserve

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	informer "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	coreLister "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	workNum       = 3
	maxRetry      = 10
	namespace     = "reservation"
	finalizerName = "reserve.kubernetes.io/finalizer"
)

type controller struct {
	client    kubernetes.Interface
	podLister coreLister.PodLister
	queue     workqueue.RateLimitingInterface
}

func (c *controller) reconcile(key string) error {
	podNamespace, podName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	pod, err := c.podLister.Pods(podNamespace).Get(podName)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// DELETION
	if pod.ObjectMeta.DeletionTimestamp.IsZero() {
		klog.Info("Not being deleted")
		if !controllerutil.ContainsFinalizer(pod, finalizerName) {
			klog.Info("Adding finalizer")
			controllerutil.AddFinalizer(pod, finalizerName)
			if _, err := c.updatePod(pod); err != nil {
				return err
			}
		}
	} else {
		// The object is being deleted
		klog.Info("Being deleted")
		if controllerutil.ContainsFinalizer(pod, finalizerName) {
			klog.Info("Reserving resources")
			if err := c.handlePodDelete(pod); err != nil {
				return err
			}
			controllerutil.RemoveFinalizer(pod, finalizerName)
			if _, err := c.updatePod(pod); err != nil {
				return err
			}
		}
		return nil
	}

	// TODO ADD & UPDATE


	return nil
}

func (c *controller) handlePodDelete(pod *corev1.Pod) error {
	// TODO Add annotations according to resources required

	return nil
}

func (c *controller) updatePod(pod *corev1.Pod) (*corev1.Pod, error) {
	return c.client.CoreV1().Pods(pod.Namespace).Update(context.Background(), pod, metav1.UpdateOptions{})
}

func (c *controller) handleErr(key string, err error) {
	// Enqueue & retry
	if c.queue.NumRequeues(key) <= maxRetry {
		c.queue.AddRateLimited(key)
		return
	}

	runtime.HandleError(err)
	c.queue.Forget(key)
}

func (c *controller) isReservable(obj interface{}) bool {
	// Get labels and judge if exists label with key == "reserve.kubernetes.io/resources"
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return false
	}

	labels := metaObj.GetLabels()
	_, found := labels["reserve.kubernetes.io/resources"]

	return found
}

func (c *controller) judgeDeployment(ownerRef metav1.OwnerReference, namespace string) bool {
	deployment, err := c.client.AppsV1().Deployments(namespace).Get(context.Background(), ownerRef.Name, metav1.GetOptions{})
	if err != nil {
		klog.Error("Failed to get deployment", err)
		return false
	}
	// pod 被删除并且 readyReplicas < Replicas —— pod重建
	if *deployment.Spec.Replicas > deployment.Status.ReadyReplicas {
		return true
	} else {
		return false
	}
}

func (c *controller) judgeStatefulSet(ownerRef metav1.OwnerReference, namespace string) bool {
	statefulset, err := c.client.AppsV1().StatefulSets(namespace).Get(context.Background(), ownerRef.Name, metav1.GetOptions{})
	if err != nil {
		klog.Error("Failed to get statefulset", err)
		return false
	}

	if *statefulset.Spec.Replicas > statefulset.Status.ReadyReplicas {
		return true
	} else {
		return false
	}
}

func (c *controller) isReconstruction(obj interface{}) bool {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return false
	}

	if len(metaObj.GetOwnerReferences()) > 0 {
		// 过滤销毁情况
		for _, ownerRef := range metaObj.GetOwnerReferences() {
			switch ownerRef.Kind {
			case "Deployment":
				return c.judgeDeployment(ownerRef, metaObj.GetNamespace())
			case "StatefulSet":
				return c.judgeStatefulSet(ownerRef, metaObj.GetNamespace())
			}
		}
	}

	return false
}

func (c *controller) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
	}
	c.queue.Add(key)
}

func (c *controller) onPodAdded(obj interface{}) {
	if !c.isReservable(obj) {
		return
	}
	c.enqueue(obj)
}

func (c *controller) onPodDeleted(obj interface{}) {
	// ! 入队条件是否有遗漏？
	// 只有打上特定标签的的 pod 才是本 controller 关心的 pod
	if !c.isReservable(obj) {
		return
	}
	// 过滤掉裸 pod 以及 pod 销毁情况
	if !c.isReconstruction(obj) {
		return
	}

	c.enqueue(obj)
}

func (c *controller) onPodUpdated(oldObj interface{}, newObj interface{}) {
	// 成功调度后 pod 会更新 spec.nodeName
	if !c.isReservable(oldObj) || !c.isReservable(newObj) {
		return
	}
	c.enqueue(newObj)
}

func (c *controller) Run(stopCh <-chan struct{}) {
	for i := 0; i < workNum; i++ {
		go wait.Until(c.worker, time.Minute, stopCh)
	}
}

func (c *controller) worker() {
	for c.processNextItem() {
	}
}

func (c *controller) processNextItem() bool {
	item, shutshown := c.queue.Get()
	if shutshown {
		return false
	}
	defer c.queue.Done(item)

	key := item.(string)

	// 调谐主逻辑
	err := c.reconcile(key)
	if err != nil {
		c.handleErr(key, err)
	}
	return true
}

func NewController(client kubernetes.Interface, podInformer informer.PodInformer) controller {
	c := controller{
		client:    client,
		podLister: podInformer.Lister(),
		queue:     workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "resourcereserve"),
	}

	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: c.onPodAdded,
		DeleteFunc: c.onPodDeleted,
		UpdateFunc: c.onPodUpdated,
	})

	return c
}
