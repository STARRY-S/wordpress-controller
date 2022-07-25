package main

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	appsinformers "k8s.io/client-go/informers/apps/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	appslisters "k8s.io/client-go/listers/apps/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

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
