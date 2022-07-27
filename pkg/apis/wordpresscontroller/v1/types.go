package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Wordpress is a specification for a Wrodpress resource
type Wordpress struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WordpressSpec   `json:"spec"`
	Status WordpressStatus `json:"status"`
}

// the Spec of the wordpress resource
type WordpressSpec struct {
	DeploymentName string `json:"deploymentName"`
	ServiceName    string `json:"serviceName"`
	Replicas       *int32 `json:"replicas"`
	DbVersion      string `json:"dbVersion"`
	WpVersion      string `json:"wpVersion"`
	DbSecretName   string `json:"dbSecretName"`
	DbSecretKey    string `json:"dbSecretKey"`
	DbPvcName      string `json:"dbPvcName"`
	WpPvcName      string `json:"wpPvcName"`
}

// the status of Wordpress resource
type WordpressStatus struct {
	AvailableReplicas int32 `json:"availableReplicas"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// WordpressList is a list of Wordpress resources
type WordpressList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Wordpress `json:"items"`
}
