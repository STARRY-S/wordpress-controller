# Wordpress controller

This project creates a wordpress **"custom resource definition"(CRD)**,
and wrote a Controller for this CRD.

Just for learning Kubernetes Controller and CRD.

## Usage

1. Install `kubernetes` and `go` on your testing enviroment.
1. Create a `Opaque` typed `Secret` with its name `mysql-passwd`, stores your mysql password in key `password`.
1. Create two PVCs, names `mysql-pvc` and `wordpress-pvc` for storing mysql and wordpress data.
1. Clone this repo, build and run.

    ``` command
    git clone https://github.com/STARRY-S/wordpress-controller.git
    cd wordpress-controller
    kubectl apply -f ./artifacs/wordpress-controller/crd.yaml
    go build -o controller .
    ./controller -kubeconfig <path/to/kube/config>

    # Open another shell
    kubectl apply -f ./artifacs/wordpress-controller/example-wordpress.yaml
    ```

1. You can access the wordpress setup page by using `port-forward`.

    ``` command
    kubectl port-forward deployments/wordpress-example 8080:80 --address='0.0.0.0'
    ```

    View `http://127.0.0.1:8080`.

----

Refer: [sample-controller](https://github.com/kubernetes/sample-controller)

> License: Apache 2.0