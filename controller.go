package main

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	appsinformers "k8s.io/client-go/informers/apps/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	appslisters "k8s.io/client-go/listers/apps/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	samplev1 "github.com/STARRY-S/wordpress-controller/pkg/apis/wordpresscontroller/v1"
	clientset "github.com/STARRY-S/wordpress-controller/pkg/generated/clientset/versioned"
	wordpressscheme "github.com/STARRY-S/wordpress-controller/pkg/generated/clientset/versioned/scheme"
	informers "github.com/STARRY-S/wordpress-controller/pkg/generated/informers/externalversions/wordpresscontroller/v1"
	listers "github.com/STARRY-S/wordpress-controller/pkg/generated/listers/wordpresscontroller/v1"
)

const controllerAgentName = "wordpress-controller"

const (
	// SuccessSynced is used as part of the Event 'reason' when a
	// Wordpress CRD is synced
	SuccessSynced = "Synced"
	// ErrResourceExists is used as part of the Event 'reason' when a
	// Wordpress CRD fails to sync due to a Deployment of the
	// same name already existing.
	ErrResourceExists = "ErrResourceExists"

	MessageResourceExists = "Resource %q already exists and is not managed by Wordpress"
	MessageResourceSynced = "Wordpress synced successfully"
)

type Controller struct {
	// standard kubernetes clientset
	kubeClientset kubernetes.Interface
	// the clientset for own API group
	wordpressClientset clientset.Interface

	deploymentsLister appslisters.DeploymentLister
	deploymentsSynced cache.InformerSynced
	wordpressLister   listers.WordpressLister
	wordpressSynced   cache.InformerSynced

	// rate limited workqueue
	workqueue workqueue.RateLimitingInterface
	// event recorder for recording event resources
	recorder record.EventRecorder
}

func NewController(
	kubeClientset kubernetes.Interface,
	wordpressClientset clientset.Interface,
	deploymentInformer appsinformers.DeploymentInformer,
	wordpressInformer informers.WordpressInformer,
) *Controller {
	// Create a event broadcaster
	// add wordpress-controller types to the default Kubernetes Scheme
	// so events can be logged for wordpress-controller types
	utilruntime.Must(wordpressscheme.AddToScheme(scheme.Scheme))
	klog.V(4).Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartStructuredLogging(0)
	eventBroadcaster.StartRecordingToSink(
		&typedcorev1.EventSinkImpl{
			Interface: kubeClientset.CoreV1().Events(""),
		},
	)
	recorder := eventBroadcaster.NewRecorder(
		scheme.Scheme, corev1.EventSource{
			Component: controllerAgentName,
		},
	)

	controller := &Controller{
		kubeClientset:      kubeClientset,
		wordpressClientset: wordpressClientset,
		deploymentsLister:  deploymentInformer.Lister(),
		deploymentsSynced:  deploymentInformer.Informer().HasSynced,
		wordpressLister:    wordpressInformer.Lister(),
		wordpressSynced:    wordpressInformer.Informer().HasSynced,
		workqueue: workqueue.NewNamedRateLimitingQueue(
			workqueue.DefaultControllerRateLimiter(),
			"Wordpress",
		),
		recorder: recorder,
	}

	klog.Info("Setting up event handlers")
	wordpressInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: controller.enqueueWordpress,
			UpdateFunc: func(old, new interface{}) {
				controller.enqueueWordpress(new)
			},
			DeleteFunc: func(obj interface{}) {},
		},
	)
	deploymentInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: controller.handleObject,
			UpdateFunc: func(oldObj, newObj interface{}) {
				newDepl := newObj.(*appsv1.Deployment)
				oldDepl := oldObj.(*appsv1.Deployment)
				if newDepl.ResourceVersion == oldDepl.ResourceVersion {
					return
				}
				controller.handleObject(newObj)
			},
			DeleteFunc: controller.handleObject,
		},
	)
	return controller
}

// Run will setup the event handlers for types we are interested in,
// as well as syncing informer caches and starting workers. It will
// block until stopCh is closed, at which point it will shutdown the workqueue
// and wait for workers to finish processing their current work items
func (c *Controller) Run(workers int, stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	klog.Info("Starting wordpress controller")

	// Wait for the caches to be synced before starting workers
	klog.Info("Waiting for informer caches to sync")
	ok := cache.WaitForCacheSync(stopCh, c.deploymentsSynced, c.wordpressSynced)
	if !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	klog.Info("Starting workers")
	// Launch two workers to process Wordpress resources
	for i := 0; i < workers; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	klog.Info("Started workers")
	<-stopCh
	klog.Info("Shutting down workers")

	return nil
}

func (c *Controller) runWorker() {
	for c.processNextWorkItem() {

	}
}

func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()

	if shutdown {
		return false
	}

	// wrap this block in a func so we can defer c.workqueue.Done
	err := func(obj interface{}) error {
		defer c.workqueue.Done(obj)
		var key string
		var ok bool

		// we expect strings to come off the workqueue, these are of the form
		// namespace/name. We do this as the delayed nature of the workqueue
		// means th items in the informer cache may actually be more up to date
		// that when the item was initially put onto the workqueue.
		if key, ok = obj.(string); !ok {
			// As the item in the workqueue is actually invalid, we call
			// forget here else we'd go into a loop of attemping to process
			// a work item that is invalid
			c.workqueue.Forget(obj)
			utilruntime.HandleError(
				fmt.Errorf("Expect string, but got %#v", obj),
			)
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// Wordpress resource to be synced
		if err := c.syncHandler(key); err != nil {
			c.workqueue.AddRateLimited(key)
			return fmt.Errorf(
				"error syncing `%s:%s`, requeuing", key, err.Error())
		}
		// Finally if no error occurs we forget this item so it does not get
		// queued again until another change happens
		c.workqueue.Forget(obj)
		klog.Infof("Successfully synced %s", key)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}
	return true
}

// syncHandler compares the actual state with the desired
func (c *Controller) syncHandler(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(
			fmt.Errorf("invalid resource key %s", key),
		)
		return nil
	}

	wp, err := c.wordpressLister.Wordpresses(namespace).Get(name)
	if err != nil {
		// the wordpress resource may no longer exist
		if errors.IsNotFound(err) {
			utilruntime.HandleError(
				fmt.Errorf("wordpress '%s' in work queue no longer exists",
					key))
			return nil
		}
		return err
	}
	deploymentName := wp.Spec.DeploymentName
	if deploymentName == "" {
		err := fmt.Errorf("%s deplotment name must be specified", key)
		utilruntime.HandleError(err)
		return nil
	}

	deployment, err := c.deploymentsLister.
		Deployments(wp.Namespace).Get(deploymentName)
	if errors.IsNotFound(err) {
		deployment, err = c.kubeClientset.
			AppsV1().Deployments(wp.Namespace).
			Create(context.TODO(), newDeployment(wp), metav1.CreateOptions{})
	}

	if err != nil {
		return err
	}

	if !metav1.IsControlledBy(deployment, wp) {
		msg := fmt.Sprintf(MessageResourceExists, deployment.Name)
		c.recorder.Event(wp, corev1.EventTypeWarning, ErrResourceExists, msg)
		return fmt.Errorf("%s", msg)
	}

	if wp.Spec.Replicas != nil &&
		*&wp.Spec.Replicas != *&deployment.Spec.Replicas {
		klog.Info("Wordpress %s replicas: %d, deployment replicas %d",
			name, *wp.Spec.Replicas, *deployment.Spec.Replicas)
		deployment, err = c.kubeClientset.AppsV1().Deployments(wp.Namespace).
			Update(context.TODO(), newDeployment(wp), metav1.UpdateOptions{})
	}

	if err != nil {
		return err
	}

	err = c.updateWordpressStatus(wp, deployment)
	if err != nil {
		return err
	}

	c.recorder.Event(wp,
		corev1.EventTypeNormal, SuccessSynced, MessageResourceSynced)
	return nil
}

func (c *Controller) updateWordpressStatus(
	wp *samplev1.Wordpress,
	deployment *appsv1.Deployment,
) error {
	// NEVER modify objects from the store. It's a read-only, local cache.
	// You can use DeepCopy() to make a deep copy of original object and modify this copy
	// Or create a copy manually for better performance
	wpCopy := wp.DeepCopy()
	wpCopy.Status.AvailableReplicas = deployment.Status.Replicas
	_, err := c.wordpressClientset.ControllerV1().Wordpresses(wp.Namespace).
		UpdateStatus(context.TODO(), wpCopy, metav1.UpdateOptions{})
	return err
}

// takes a wordpress resource and converts it into a namespace/name
func (c *Controller) enqueueWordpress(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.workqueue.Add(key)
}

func (c *Controller) handleObject(obj interface{}) {
	var object metav1.Object
	var ok bool
	if object, ok = obj.(metav1.Object); !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(
				fmt.Errorf("error decoding object, invalid type"))
			return
		}
		object, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			err := fmt.Errorf("error decoding object tombstone, invalid type")
			utilruntime.HandleError(err)
			return
		}
		klog.Infof(
			"received deleted object %s from tombstone", object.GetName())
	}
	klog.Infof("Processing object %s", object.GetName())
	if ownerRef := metav1.GetControllerOf(object); ownerRef != nil {
		if ownerRef.Kind != "Wordpress" {
			return
		}

		wp, err := c.wordpressLister.Wordpresses(object.GetNamespace()).
			Get(ownerRef.Name)
		if err != nil {
			klog.Infof("ignoring orphaned object %s/%s of wordpress %s",
				object.GetNamespace(), object.GetName(), ownerRef.Name)
			return
		}
		c.enqueueWordpress(wp)
		return
	}
}

// newDeployment creates a new deployment for wordpress resource

func newDeployment(wp *samplev1.Wordpress) *appsv1.Deployment {
	labels := map[string]string{
		"app":        "nginx",
		"controller": wp.Name,
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wp.Spec.DeploymentName,
			Namespace: wp.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(
					wp, samplev1.SchemeGroupVersion.WithKind("Wordpress")),
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: wp.Spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "nginx",
							Image: "nginx:1.14.2",
						},
					},
				},
			},
		},
	}
}
