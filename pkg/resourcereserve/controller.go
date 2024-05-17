package resourcereserve

import (
	"context"
	"encoding/json"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
	workNum           = 1
	maxRetry          = 10
	namespace         = "reservation"
	finalizerName     = "reserve.kubernetes.io/finalizer"
	annotationKeyName = "reserve.kubernetes.io/resources"
	configMapName     = "pod-node-info"
)

type controller struct {
	client    kubernetes.Interface
	podLister coreLister.PodLister
	queue     workqueue.RateLimitingInterface
}

func (c *controller) reconcile(key string) error {
	klog.Info("Reconciling")
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
		// 如果是管理 pod 的 OwnerReference 被删除，则不做处理，直接删 finalizers
		if c.isOwnerBeingDeleted(pod) {
			klog.Info("pod is being destroyed due to ownerReference deleted")
			if controllerutil.ContainsFinalizer(pod, finalizerName) {
				controllerutil.RemoveFinalizer(pod, finalizerName)
				if _, err := c.updatePod(pod); err != nil {
					return err
				}
			}
			return nil
		}
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

	// ADD & UPDATE
	// 判断pod是否调度成功
	if pod.Spec.NodeName == "" {
		// ADD 到此为止
		klog.Info("Scheduling or schedule failed")
		return nil
	}
	// 调度成功 —— Update
	// 获取 pod 的 ownerReference，Deployment 和 StatefulSet 分别处理
	ownerRefList := pod.ObjectMeta.OwnerReferences
	if len(ownerRefList) == 0 {
		klog.Info("Pod has no owner reference")
		return nil
	}
	// for _, ownerRef := range ownerRefList {
	// 	switch ownerRef.Kind {
	// 	case "Deployment":
	// 		c.syncDeployment()
	// 	case "StatefulSet":
	// 		c.syncStatefulSet(pod)
	// 	}
	// }

	// 采用统一策略
	if err := c.syncNodeAnnotation(pod); err != nil {
		klog.Error("Failed to sync node annotation: ", err)
		return err
	}

	return nil
}

// 调度完成后，删除之前为之预留的资源
func (c *controller) syncNodeAnnotation(pod *corev1.Pod) error {
	// 尝试获取 configMap
	configMap, err := c.client.CoreV1().ConfigMaps(namespace).Get(context.Background(), configMapName, metav1.GetOptions{})
	if err != nil {
		// Create the ConfigMap if it doesn't exist
		klog.Info("ConfigMap not found, creating...")
		if errors.IsNotFound(err) {
			configMap = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configMapName,
					Namespace: namespace,
				},
				Data: make(map[string]string),
			}
			_, err := c.client.CoreV1().ConfigMaps(namespace).Create(context.Background(), configMap, metav1.CreateOptions{})
			if err != nil {
				klog.Error("Failed to create configMap: ", err)
				return err
			}
		} else {
			klog.Error("Failed to get configMap: ", err)
			return err
		}
	}
	// 1. 查找原 pod 的 nodeName
	ownerUid := pod.ObjectMeta.OwnerReferences[0].UID
	ownerUidNodeInfo := configMap.Data
	if ownerUidNodeInfo == nil {
		// If it's nil, we need to initialize it
		ownerUidNodeInfo = make(map[string]string)
	}
	if _, ok := ownerUidNodeInfo[string(ownerUid)]; !ok {
		klog.Info("Did not find ownerUid as a key in configMap")
		return err
	}
	nodeNameList, err := deserializeStringSlice(ownerUidNodeInfo[string(ownerUid)])
	if err != nil {
		klog.Error("Failed to deserialize string slice: ", err)
		return err
	}
	nodeName := nodeNameList[0]
	if len(nodeNameList) == 1 {
		delete(ownerUidNodeInfo, string(ownerUid))
	} else {
		nodeNameList = nodeNameList[1:]
		ownerUidNodeInfo[string(ownerUid)], err = serializeStringSlice(nodeNameList)
		if err != nil {
			klog.Error("Failed to serialize string slice: ", err)
			return err
		}
	}
	configMap.Data = ownerUidNodeInfo
	if _, err = c.updateConfigMap(configMap); err != nil {
		klog.Error("Failed to update configMap: ", err)
		return err
	}
	// 2. 获取 node 更新 Annotation
	node, err := c.client.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
	if err != nil {
		klog.Error("Failed to get node: ", err)
		return err
	}
	annotationValue, exists := node.Annotations[annotationKeyName]
	if !exists {
		klog.Info("Annotation ", annotationKeyName, " does not exist on node ", node.GetName())
		return err
	}

	// Parse the value
	var reservedResources []ReservationItem
	err = json.Unmarshal([]byte(annotationValue), &reservedResources)
	if err != nil {
		klog.Error("Failed to unmarshal annotation value: ", err)
		return err
	}

	// Update the annotation
	updateReservedResources(&reservedResources, string(ownerUid), pod)

	// Clean zero items
	if len(reservedResources) == 0 {
		delete(node.Annotations, annotationKeyName)
	} else {
		// Update the node
		annotationValueJson, err := json.Marshal(reservedResources)
		if err != nil {
			klog.Error("Failed to marshal annotation value: ", err)
			return err
		}
		node.Annotations[annotationKeyName] = string(annotationValueJson)
	}

	_, err = c.updateNode(node)
	if err != nil {
		klog.Error("Failed to update node: ", err)
		return err
	}

	return nil
}

func (c *controller) handlePodDelete(pod *corev1.Pod) error {
	klog.Info("Handling Pod Delete(reserving resources)")
	ownerUid := string(pod.ObjectMeta.OwnerReferences[0].UID)

	// Retrieve the name of the node where the pod is scheduled
	nodeName := pod.Spec.NodeName

	// Get the Node object using nodeName
	node, err := c.client.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err != nil {
		klog.Error("Failed to get node in func handlePodDelete")
		return err
	}

	// Get current annotations of the node
	annotations := node.ObjectMeta.Annotations
	if annotations == nil {
		annotations = make(map[string]string)
	}

	// Declare and initialize the total CPU and Memory requests
	totalCPURequest := int64(0)
	totalMemoryRequest := int64(0)

	// Calculate the total CPU and Memory requests by summing the requests of all containers in the Pod
	for _, container := range pod.Spec.Containers {
		totalCPURequest += container.Resources.Requests.Cpu().MilliValue()
		totalMemoryRequest += container.Resources.Requests.Memory().Value()
	}
	klog.Info("Total CPU request: ", totalCPURequest)
	klog.Info("Total Memory request: ", totalMemoryRequest)

	// Get and unmarshal the existing reservations from the annotations
	var ReservationList []ReservationItem
	found := false
	if value, exists := annotations[annotationKeyName]; exists {
		// Unmarshalling the annotations
		if err := json.Unmarshal([]byte(value), &ReservationList); err != nil {
			klog.Error("Failed to unmarshal annotation value: ", err)
			return err
		}

		// Loop through existing reservations
		for i, reservation := range ReservationList {
			if reservation.OwnerUID == ownerUid {
				// We found a reservation from the same OwnerUID
				found = true

				klog.Info("Before reserving for the pod")
				printReservedResources(&reservation)

				for j, resource := range reservation.ReservedResources {
					if resource.ResourceType == "cpu" {
						ReservationList[i].ReservedResources[j].ReservedQuantity += totalCPURequest
					} else if resource.ResourceType == "memory" {
						ReservationList[i].ReservedResources[j].ReservedQuantity += totalMemoryRequest
					}
				}

				klog.Info("After reserving for the pod")
				printReservedResources(&reservation)
				break
			}
		}
	}

	// if no matching reservation is found, create a new one
	if !found {
		klog.Info("No matching reservation found, creating a new one")
		ReservationList = append(ReservationList, ReservationItem{
			OwnerType: pod.ObjectMeta.OwnerReferences[0].Kind,
			OwnerUID:  ownerUid,
			PodName:   pod.Name,
			ReservedResources: []ReservedResource{
				{
					ResourceType:     "cpu",
					ReservedQuantity: totalCPURequest,
				},
				{
					ResourceType:     "memory",
					ReservedQuantity: totalMemoryRequest,
				},
			},
		})
	}
	newReservationListJson, err := json.Marshal(ReservationList)
	if err != nil {
		klog.Error("Failed to marshal annotation value: ", err)
		return err
	}

	annotations[annotationKeyName] = string(newReservationListJson)

	// Update the node with the new annotations
	node.ObjectMeta.Annotations = annotations
	_, err = c.updateNode(node)
	if err != nil {
		klog.Error("Failed to update node: ", err)
		return err
	}

	// 在 configMap 中添加 [ownerUid]nodeName
	// 尝试获取 configMap
	configMap, err := c.client.CoreV1().ConfigMaps(namespace).Get(context.Background(), configMapName, metav1.GetOptions{})
	if err != nil {
		// Create the ConfigMap if it doesn't exist
		klog.Info("ConfigMap not found, creating...")
		if errors.IsNotFound(err) {
			configMap = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configMapName,
					Namespace: namespace,
				},
				Data: make(map[string]string),
			}
			_, err := c.client.CoreV1().ConfigMaps(namespace).Create(context.Background(), configMap, metav1.CreateOptions{})
			if err != nil {
				klog.Error("Failed to create configMap: ", err)
				return err
			}
		} else {
			klog.Error("Failed to get configMap: ", err)
			return err
		}
	}
	ownerUidNodeInfo := configMap.Data
	if ownerUidNodeInfo == nil {
		// If it's nil, we need to initialize it
		ownerUidNodeInfo = make(map[string]string)
	}
	if _, ok := ownerUidNodeInfo[string(ownerUid)]; !ok {
		klog.Info("Add new item to configMap")
		newNodeNameList := make([]string, 0)
		newNodeNameList = append(newNodeNameList, nodeName)
		ownerUidNodeInfo[string(ownerUid)], err = serializeStringSlice(newNodeNameList)
		if err != nil {
			klog.Error("Failed to serialize string slice: ", err)
			return err
		}
	} else {
		klog.Info("Append nodeName to existing item of configMap")
		nodeNameList, err := deserializeStringSlice(ownerUidNodeInfo[string(ownerUid)])
		if err != nil {
			klog.Error("Failed to deserialize string slice: ", err)
			return err
		}
		nodeNameList = append(nodeNameList, nodeName)
		ownerUidNodeInfo[string(ownerUid)], err = serializeStringSlice(nodeNameList)
		if err != nil {
			klog.Error("Failed to serialize string slice: ", err)
			return err
		}
	}
	configMap.Data = ownerUidNodeInfo
	if _, err = c.updateConfigMap(configMap); err != nil {
		klog.Error("Failed to update configMap: ", err)
		return err
	}

	klog.Info("Finish Reserving, take a look at the annotations~")

	return nil
}

func (c *controller) updateNode(node *corev1.Node) (*corev1.Node, error) {
	return c.client.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{})
}

func (c *controller) updatePod(pod *corev1.Pod) (*corev1.Pod, error) {
	return c.client.CoreV1().Pods(pod.Namespace).Update(context.Background(), pod, metav1.UpdateOptions{})
}

func (c *controller) updateConfigMap(configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	return c.client.CoreV1().ConfigMaps(configMap.Namespace).Update(context.Background(), configMap, metav1.UpdateOptions{})
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
		klog.Error("Failed to get deployment: ", err)
		return false
	}
	// pod 被删除并且 readyReplicas < Replicas —— pod重建
	if *deployment.Spec.Replicas > deployment.Status.ReadyReplicas {
		klog.Info("Pod is reconstructing")
		return true
	} else {
		return false
	}
}

func (c *controller) judgeStatefulSet(ownerRef metav1.OwnerReference, namespace string) bool {
	statefulset, err := c.client.AppsV1().StatefulSets(namespace).Get(context.Background(), ownerRef.Name, metav1.GetOptions{})
	if err != nil {
		klog.Error("Failed to get statefulset: ", err)
		return false
	}

	if *statefulset.Spec.Replicas > statefulset.Status.ReadyReplicas {
		klog.Info("Pod is reconstructing")
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

func (c *controller) isOwnerBeingDeleted(obj interface{}) bool {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return false
	}
	for _, ref := range metaObj.GetOwnerReferences() {
		switch ref.Kind {
		case "ReplicaSet":
			// get ReplicaSet
			klog.Info("Trying to get OwnerReference ", ref.Name)
			_, err := c.client.AppsV1().ReplicaSets(namespace).Get(context.TODO(), ref.Name, metav1.GetOptions{})
			if err != nil {
				return true
			}
		case "StatefulSet":
			_, err := c.client.AppsV1().StatefulSets(namespace).Get(context.TODO(), ref.Name, metav1.GetOptions{})
			if err != nil {
				return true
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
	klog.Info("Pod added")
	c.enqueue(obj)
}

func (c *controller) onPodDeleted(obj interface{}) {
	// ! 入队条件是否有遗漏？
	// 只有打上特定标签的的 pod 才是本 controller 关心的 pod
	if !c.isReservable(obj) {
		return
	}
	// 过滤掉裸 pod 以及由于 replicas 减少导致 pod 销毁情况
	if !c.isReconstruction(obj) {
		return
	}
	// 过滤掉 pod 的 ownerReference 删除导致 pod 销毁情况
	// if c.isOwnerBeingDeleted(obj) {
	// 	klog.Info("Owner is being deleted")
	// 	return
	// }

	klog.Info("Pod deleted")
	c.enqueue(obj)
}

func (c *controller) onPodUpdated(oldObj interface{}, newObj interface{}) {
	// 成功调度后 pod 会更新 spec.nodeName
	if !c.isReservable(oldObj) || !c.isReservable(newObj) {
		return
	}
	// 过滤掉 pod 的 ownerReference 删除导致 pod 销毁情况
	// if c.isOwnerBeingDeleted(newObj) {
	// 	klog.Info("Owner is being deleted")
	// 	return
	// }
	klog.Info("Pod updated")
	c.enqueue(newObj)
}

func (c *controller) Run(stopCh <-chan struct{}) {
	klog.Info("Running")
	for i := 0; i < workNum; i++ {
		go wait.Until(c.worker, time.Minute, stopCh)
	}
	<-stopCh
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
		AddFunc:    c.onPodAdded,
		DeleteFunc: c.onPodDeleted,
		UpdateFunc: c.onPodUpdated,
	})

	return c
}
