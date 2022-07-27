package main

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	appsinformers "k8s.io/client-go/informers/apps/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	wordpressv1 "github.com/STARRY-S/wordpress-controller/pkg/apis/wordpresscontroller/v1"
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

const (
	MysqlVolume    = "wordpress-mysql-volume"
	HtmlVolume     = "wordpress-html-volume"
	MysqlPortName  = "mysql"
	HttpPortName   = "wordpress-http"
	HttpsPortName  = "wordpress-https"
	MysqlPortValue = 3306
	HttpPortValue  = 80
	HttpsPortValue = 443
	AppName        = "Wordpress"
)

type Controller struct {
	// standard kubernetes clientset
	kubeClientset kubernetes.Interface
	// the clientset for own API group
	wordpressClientset clientset.Interface

	// deploymentsLister lists all deployments
	deploymentsLister appslisters.DeploymentLister
	// InformerSynced is a function that can be used to determine
	// if an informer has synced.
	// This is useful for determining if caches have synced.
	deploymentsSynced cache.InformerSynced

	servicesLister corelisters.ServiceLister

	servicesSynced cache.InformerSynced

	// wordpressesLister lists all wordpress resources
	wordpressesLister listers.WordpressLister
	// InformerSynced is a function that can be used to determine
	// if an informer has synced.
	// This is useful for determining if caches have synced.
	wordpressesSynced cache.InformerSynced

	// rate limited workqueue
	workqueue workqueue.RateLimitingInterface
	// event recorder for recording event resources
	recorder record.EventRecorder
}

func NewController(
	kubeClientset kubernetes.Interface,
	wordpressClientset clientset.Interface,
	deploymentInformer appsinformers.DeploymentInformer,
	serviceInformer coreinformers.ServiceInformer,
	wordpressInformer informers.WordpressInformer,
) *Controller {
	// Create a event broadcaster
	// add wordpress-controller types to the default Kubernetes Scheme
	// so events can be logged for wordpress-controller types
	utilruntime.Must(wordpressscheme.AddToScheme(scheme.Scheme))
	klog.Info("Creating event broadcaster")
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
		wordpressesLister:  wordpressInformer.Lister(),
		wordpressesSynced:  wordpressInformer.Informer().HasSynced,
		servicesLister:     serviceInformer.Lister(),
		servicesSynced:     serviceInformer.Informer().HasSynced,
		workqueue: workqueue.NewNamedRateLimitingQueue(
			workqueue.DefaultControllerRateLimiter(),
			"Wordpresses",
		),
		recorder: recorder,
	}

	klog.Info("Setting up event handlers")
	wordpressInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: controller.enqueueWordpress,
			UpdateFunc: func(old, new interface{}) {
				klog.Info("wpInformer: UpdateFunc triggered")
				controller.enqueueWordpress(new)
			},
			DeleteFunc: func(obj interface{}) {
				klog.Info("wpInformer: delete func triggered!")
			},
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
				klog.Info("dpInformer UpdateFunc triggered")
				controller.handleObject(newObj)
			},
			DeleteFunc: controller.handleObject,
		},
	)
	serviceInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: controller.handleObject,
			UpdateFunc: func(oldObj, newObj interface{}) {
				newSvc := newObj.(*corev1.Service)
				oldSvc := newObj.(*corev1.Service)
				if newSvc.ResourceVersion == oldSvc.ResourceVersion {
					return
				}
				klog.Info("svcInformer UpdateFunc triggered")
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
	ok := cache.WaitForCacheSync(
		stopCh, c.deploymentsSynced, c.wordpressesSynced)
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

	wp, err := c.wordpressesLister.Wordpresses(namespace).Get(name)
	if err != nil {
		// the wordpress resource may no longer exist
		if errors.IsNotFound(err) {
			utilruntime.HandleError(
				fmt.Errorf("wordpress '%s' in work queue no longer exists",
					key))
			return nil
		}
		klog.Error("Get failed")
		return err
	}
	deploymentName := wp.Spec.DeploymentName
	if deploymentName == "" {
		err := fmt.Errorf("%s deplotment name must be specified", key)
		utilruntime.HandleError(err)
		return nil
	}

	serviceName := wp.Spec.ServiceName
	if serviceName == "" {
		err := fmt.Errorf("%s service name must be specified", key)
		utilruntime.HandleError(err)
		return nil
	}

	dbVersion := wp.Spec.DbVersion
	if dbVersion == "" {
		err := fmt.Errorf("%s dbVersion must be specified", key)
		utilruntime.HandleError(err)
		return nil
	}

	wpVersion := wp.Spec.WpVersion
	if wpVersion == "" {
		err := fmt.Errorf("%s wpVersion must be specified", key)
		utilruntime.HandleError(err)
		return nil
	}

	dbSecretName := wp.Spec.DbSecretName
	if dbSecretName == "" {
		err := fmt.Errorf("%s dbSecretName must be specified", key)
		utilruntime.HandleError(err)
		return nil
	}

	dbSecretKey := wp.Spec.DbSecretKey
	if dbSecretKey == "" {
		err := fmt.Errorf("%s dbSecretKey must be specified", key)
		utilruntime.HandleError(err)
		return nil
	}

	deployment, err := c.deploymentsLister.
		Deployments(wp.Namespace).Get(deploymentName)
	if errors.IsNotFound(err) {
		klog.Info("wordpress deployment not found, creating")
		deployment, err = c.kubeClientset.
			AppsV1().Deployments(wp.Namespace).
			Create(context.TODO(), newDeployment(wp), metav1.CreateOptions{})
	}
	if err != nil {
		klog.Error("failed to create deployment")
		return err
	}

	service, err := c.servicesLister.
		Services(wp.Namespace).Get(serviceName)
	if errors.IsNotFound(err) {
		klog.Info("wordpress service not found, creating")
		service, err = c.kubeClientset.CoreV1().Services(wp.Namespace).
			Create(context.TODO(), newService(wp), metav1.CreateOptions{})
	}
	if err != nil {
		klog.Error("failed to create service")
		return err
	}

	if !metav1.IsControlledBy(deployment, wp) {
		msg := fmt.Sprintf(MessageResourceExists, deployment.Name)
		c.recorder.Event(wp, corev1.EventTypeWarning, ErrResourceExists, msg)
		return fmt.Errorf("%s", msg)
	}

	if !metav1.IsControlledBy(service, wp) {
		msg := fmt.Sprintf(MessageResourceExists, service.Name)
		c.recorder.Event(wp, corev1.EventTypeWarning, ErrResourceExists, msg)
		return fmt.Errorf("%s", msg)
	}

	if wp.Spec.Replicas != nil &&
		*wp.Spec.Replicas != *deployment.Spec.Replicas {
		klog.Infof("Wordpress %s replicas: %d, deployment replicas %d",
			name, *wp.Spec.Replicas, *deployment.Spec.Replicas)
		deployment, err = c.kubeClientset.AppsV1().Deployments(wp.Namespace).
			Update(context.TODO(), newDeployment(wp), metav1.UpdateOptions{})
		service, err = c.kubeClientset.CoreV1().Services(wp.Namespace).
			Update(context.TODO(), newService(wp), metav1.UpdateOptions{})
	}

	if err != nil {
		klog.Error("Update failed")
		return err
	}

	// Finally, we update the status block of the Foo resource to reflect the
	// current state of the world
	err = c.updateWordpressStatus(wp, deployment)
	if err != nil {
		klog.Error("updateWordpressStatus failed")
		return err
	}

	c.recorder.Event(wp,
		corev1.EventTypeNormal, SuccessSynced, MessageResourceSynced)
	return nil
}

func (c *Controller) updateWordpressStatus(
	wp *wordpressv1.Wordpress,
	deployment *appsv1.Deployment,
) error {
	// NEVER modify objects from the store. It's a read-only, local cache.
	// You can use DeepCopy() to make a deep copy of original object and modify
	// this copy or create a copy manually for better performance
	wpCopy := wp.DeepCopy()
	wpCopy.Status.AvailableReplicas = deployment.Status.Replicas
	wpInterface := c.wordpressClientset.ControllerV1().Wordpresses(wp.Namespace)
	_, err := wpInterface.
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

		wp, err := c.wordpressesLister.
			Wordpresses(object.GetNamespace()).Get(ownerRef.Name)
		if err != nil {
			klog.Infof("ignoring orphaned object %s/%s of wordpress %s",
				object.GetNamespace(), object.GetName(), ownerRef.Name)
			return
		}
		c.enqueueWordpress(wp)
		return
	}
}

// newDeployment creates a new deployment for wordpress and mysql resource
func newDeployment(wp *wordpressv1.Wordpress) *appsv1.Deployment {
	labels := map[string]string{
		"app":        AppName,
		"controller": wp.Name,
	}
	volumes := []corev1.Volume{
		{
			Name: MysqlVolume,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: wp.Spec.DbPvcName,
					ReadOnly:  false,
				},
			},
		},
		{
			Name: HtmlVolume,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: wp.Spec.WpPvcName,
					ReadOnly:  false,
				},
			},
		},
	}
	containers := []corev1.Container{
		{
			Name:  "wordpress",
			Image: "wordpress:" + wp.Spec.WpVersion,
			Ports: []corev1.ContainerPort{
				{
					Name:          HttpPortName,
					ContainerPort: HttpPortValue,
					Protocol:      corev1.ProtocolTCP,
				},
				{
					Name:          HttpsPortName,
					ContainerPort: HttpsPortValue,
					Protocol:      corev1.ProtocolTCP,
				},
			},
			Env: []corev1.EnvVar{
				{
					Name:  "WORDPRESS_DB_HOST",
					Value: "127.0.0.1", // do not use localhost here
				},
				{
					Name: "WORDPRESS_DB_PASSWORD",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: wp.Spec.DbSecretName,
							},
							Key: wp.Spec.DbSecretKey,
						},
					},
				},
			},

			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      HtmlVolume,
					ReadOnly:  false,
					MountPath: "/var/www/html",
				},
			},
		},
		{
			Name:  "mysql",
			Image: "mysql:" + wp.Spec.DbVersion,
			Ports: []corev1.ContainerPort{
				{
					Name: "mysql",
					// HostPort is 0 since we do not expose mysql database
					// outside of the pod
					ContainerPort: MysqlPortValue,
					Protocol:      corev1.ProtocolTCP,
				},
			},
			Env: []corev1.EnvVar{
				{
					Name: "MYSQL_ROOT_PASSWORD",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: wp.Spec.DbSecretName,
							},
							Key: wp.Spec.DbSecretKey,
						},
					},
				},
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      MysqlVolume,
					ReadOnly:  false,
					MountPath: "/var/lib/mysql",
				},
			},
		},
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wp.Name,
			Namespace: wp.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(
					wp, wordpressv1.SchemeGroupVersion.WithKind("Wordpress")),
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
					Containers: containers,
					Volumes:    volumes,
				},
			},
		},
	}
}

// Create a ClusterIP service for wordpress web resource
func newService(wp *wordpressv1.Wordpress) *corev1.Service {
	service := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wp.Spec.ServiceName,
			Namespace: wp.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(
					wp, wordpressv1.SchemeGroupVersion.WithKind("Wordpress")),
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:     HttpPortName,
					Protocol: corev1.ProtocolTCP,
					Port:     80, // exposed port
					// the port to access on the pods targeted by the service
					TargetPort: intstr.IntOrString{
						StrVal: HttpPortName,
					},
				},
				{
					Name:     HttpsPortName,
					Protocol: corev1.ProtocolTCP,
					Port:     443, // exposed port
					// the port to access on the pods targeted by the service
					TargetPort: intstr.IntOrString{
						StrVal: HttpsPortName,
					},
				},
			},
			Type: corev1.ServiceTypeClusterIP,
			Selector: map[string]string{
				"app": AppName,
			},
		},
	}
	return &service
}

// // Create new ingress for wordpress service
// func newIngress(wp *wordpressv1.Wordpress) *extensionsv1beta1.Ingress {
// 	pathType := extensionsv1beta1.PathTypePrefix
// 	ingress := extensionsv1beta1.Ingress{
// 		ObjectMeta: metav1.ObjectMeta{
// 			Name:      wp.Name + "-ingress",
// 			Namespace: wp.Namespace,
// 			OwnerReferences: []metav1.OwnerReference{
// 				*metav1.NewControllerRef(
// 					wp, wordpressv1.SchemeGroupVersion.WithKind("Wordpress")),
// 			},
// 		},
// 		Spec: extensionsv1beta1.IngressSpec{
// 			Rules: []extensionsv1beta1.IngressRule{
// 				{
// 					IngressRuleValue: extensionsv1beta1.IngressRuleValue{
// 						HTTP: &extensionsv1beta1.HTTPIngressRuleValue{
// 							Paths: []extensionsv1beta1.HTTPIngressPath{
// 								{
// 									Path: "/",
// 									Backend: extensionsv1beta1.IngressBackend{
// 										ServiceName: wp.Name + "-service",
// 										ServicePort: intstr.IntOrString{
// 											StrVal: HttpPortName,
// 										},
// 									},
// 									PathType: &pathType,
// 								},
// 							},
// 						},
// 					},
// 				},
// 			},
// 		},
// 	}
// 	return &ingress
// }
