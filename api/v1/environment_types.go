package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnvironmentSpec defines the desired state of Environment
type EnvironmentSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=44
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9\-]*[a-z0-9])?$`
	// Id is the identifier for the environment, used to generate the namespace name
	Id string `json:"id"`

	// Annotations to apply to the created namespace
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Labels to apply to the created namespace
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// EnvironmentPhase represents the current phase of the Environment
type EnvironmentPhase string

const (
	// PhaseCreating indicates the environment is being created
	PhaseCreating EnvironmentPhase = "Creating"
	// PhaseReady indicates the environment is ready for use
	PhaseReady EnvironmentPhase = "Ready"
	// PhaseCreateFailed indicates the environment creation failed
	PhaseCreateFailed EnvironmentPhase = "CreateFailed"
	// PhaseDeleting indicates the environment is being deleted
	PhaseDeleting EnvironmentPhase = "Deleting"
	// PhaseUpdating indicates the environment is being updated
	PhaseUpdating EnvironmentPhase = "Updating"
	// PhaseUpdateFailed indicates the environment update failed
	PhaseUpdateFailed EnvironmentPhase = "UpdateFailed"
)

// SubResourcePhase represents the lifecycle phase of a managed sub-resource (Namespace, RoleBinding)
type SubResourcePhase string

const (
	// PhaseStatePending indicates the resource has not been processed yet.
	PhaseStatePending SubResourcePhase = "Pending"
	// PhaseStateCreating indicates the resource is being created.
	PhaseStateCreating SubResourcePhase = "Creating"
	// PhaseStateReady indicates the resource exists and is configured.
	PhaseStateReady SubResourcePhase = "Ready"
	// PhaseStateTerminating indicates the resource is being deleted.
	PhaseStateTerminating SubResourcePhase = "Terminating"
	// PhaseStateFailed indicates the resource encountered an error.
	PhaseStateFailed SubResourcePhase = "Failed"
	// PhaseStateUnmanaged indicates the resource exists but is not managed by the operator.
	PhaseStateUnmanaged SubResourcePhase = "Unmanaged"
	// PhaseStateDeleted indicates the resource has been deleted.
	PhaseStateDeleted SubResourcePhase = "Deleted"
)

// EnvironmentStatus defines the observed state of Environment
type EnvironmentStatus struct {
	// Phase represents the current lifecycle phase of the environment
	// +optional
	Phase EnvironmentPhase `json:"phase,omitempty"`

	// Namespace is the name of the managed Kubernetes namespace
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// ErrorMessage provides details on the last error encountered
	// +optional
	ErrorMessage string `json:"errorMessage,omitempty"`

	// NamespacePhase indicates the current state of the managed namespace.
	// +optional
	NamespacePhase string `json:"namespacePhase,omitempty"`

	// RoleBindingPhase indicates the current state of the managed role binding.
	// +optional
	RoleBindingPhase string `json:"roleBindingPhase,omitempty"`

	// Conditions provide specific status details
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Namespace",type="string",JSONPath=".status.namespace",description="The provisioned namespace"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="Current phase"
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Ready status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Namespaced,singular=environment,shortName=env
// Environment represents a request to create and manage an isolated Kubernetes namespace
type Environment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EnvironmentSpec   `json:"spec,omitempty"`
	Status EnvironmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// EnvironmentList contains a list of Environment resources
type EnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Environment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Environment{}, &EnvironmentList{})
}
