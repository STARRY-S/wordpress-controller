package main

import (
	"flag"
	"time"

	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	wpclientset "github.com/STARRY-S/wordpress-controller/pkg/generated/clientset/versioned"
	informers "github.com/STARRY-S/wordpress-controller/pkg/generated/informers/externalversions"
	"github.com/STARRY-S/wordpress-controller/pkg/signals"
)

var (
	masterURL  string
	kubeconfig string
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	// setup signals so we handle the first shutdown signal gracefully
	stopCh := signals.SetupSignalHandler()

	cfg, err := clientcmd.BuildConfigFromFlags(masterURL, kubeconfig)
	if err != nil {
		klog.Fatalf("Error build kubeconfig: %s", err.Error())
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		klog.Fatalf("Error building kubernetes clientset: %s", err.Error())
	}

	wordpressClient, err := wpclientset.NewForConfig(cfg)
	if err != nil {
		klog.Fatalf("Error building example clientset: %s", err.Error())
	}

	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(
		kubeClient, time.Second*30)
	wordpressInformerFactory := informers.NewSharedInformerFactory(
		wordpressClient, time.Second*30)
	// serviceInformerFactory := informers.NewSharedInformerFactory()

	controller := NewController(
		kubeClient, wordpressClient,
		kubeInformerFactory.Apps().V1().Deployments(),
		kubeInformerFactory.Core().V1().Services(),
		wordpressInformerFactory.Controller().V1().Wordpresses(),
	)

	kubeInformerFactory.Start(stopCh)
	wordpressInformerFactory.Start(stopCh)

	if err = controller.Run(2, stopCh); err != nil {
		klog.Fatalf("Error running controller: %s", err.Error())
	}
}

func init() {
	flag.StringVar(
		&kubeconfig, "kubeconfig", "", "Path to kube config")
	flag.StringVar(
		&masterURL, "master", "", "kubernetes API server")
}
