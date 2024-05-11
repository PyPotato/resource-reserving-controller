package resourcereserve

import (
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	informer "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	coreLister "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

const (
	workNum = 3
	maxRetry = 10
	namespace = "reservation"
)

type controller struct {
	client    kubernetes.Interface
	podLister coreLister.PodLister
	queue     workqueue.RateLimitingInterface
}

func (c *controller) updatePod(old, new interface{}) {
	
}

func (c *controller) deletePod(obj interface{}) {

}



func (c *controller) Run(stopCh <-chan struct{}) {
	for i := 0; i < workNum; i ++ {
		go wait.Until(c.worker, time.Minute, stopCh)
	}
}

func (c *controller) worker() {
	for c.processNextItem() {}
}

func (c *controller) processNextItem() bool {
	item, shutshown := c.queue.Get()
	if shutshown {
		return false
	}
	defer c.queue.Done(item)

	key := item.(string)

	//
	err := c.reconcile(key)
	if err != nil {
		c.handleErr(key, err)
	}
	return true
}

func (c *controller) reconcile(key string) error {
	//TODO 调谐逻辑

	return nil
}

func (c *controller) handleErr(key string, err error) {
	//TODO Enqueue & retry
	
}

func NewController(client kubernetes.Interface, podInformer informer.PodInformer) controller {
	c := controller {
		client: client,
		podLister: podInformer.Lister(),
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "resourcereserve"),
	}

	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: c.updatePod,
		DeleteFunc: c.deletePod,
	})

	return c
}
