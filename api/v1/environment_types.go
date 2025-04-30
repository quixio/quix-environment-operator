// +kubebuilder:object:generate=true
// +groupName=quix.io
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
	// All annotation keys must have the 'quix.io/' prefix
	// +optional
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.startsWith('quix.io/'))",message="All annotation keys must have the 'quix.io/' prefix"
	// +kubebuilder:validation:XValidation:rule="!self.exists(k, k == 'quix.io/created-by' || k == 'quix.io/environment-crd-namespace' || k == 'quix.io/environment-resource-name')",message="Cannot override protected annotations: quix.io/created-by, quix.io/environment-crd-namespace, quix.io/environment-resource-name"
	Annotations map[string]string `json:"annotations,omitempty"`

	// Labels to apply to the created namespace
	// All label keys must have the 'quix.io/' prefix
	// +optional
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.startsWith('quix.io/'))",message="All label keys must have the 'quix.io/' prefix"
	// +kubebuilder:validation:XValidation:rule="!self.exists(k, k == 'quix.io/managed-by' || k == 'quix.io/environment-id' || k == 'quix.io/environment-name')",message="Cannot override protected labels: quix.io/managed-by, quix.io/environment-id, quix.io/environment-name"
	Labels map[string]string `json:"labels,omitempty"`
}

// EnvironmentPhase represents the current phase of the Environment
// +kubebuilder:validation:Enum=InProgress;Ready;Failed;Deleting
type EnvironmentPhase string

const (
	// EnvironmentPhaseInProgress indicates the environment is in progress
	EnvironmentPhaseInProgress EnvironmentPhase = "InProgress"
	// EnvironmentPhaseReady indicates the environment is ready for use
	EnvironmentPhaseReady EnvironmentPhase = "Ready"
	// EnvironmentPhaseFailed indicates the environment creation failed
	EnvironmentPhaseFailed EnvironmentPhase = "Failed"
	// EnvironmentPhaseDeleting indicates the environment is being deleted
	EnvironmentPhaseDeleting EnvironmentPhase = "Deleting"
)

// ResourceStatusPhase represents the current phase of a managed sub-resource
// +kubebuilder:validation:Enum=Creating;Active;Failed;Deleting
type ResourceStatusPhase string

const (
	// ResourceStatusPhaseCreating indicates the resource is being created
	ResourceStatusPhaseCreating ResourceStatusPhase = "Creating"
	// ResourceStatusPhaseActive indicates the resource is active
	ResourceStatusPhaseActive ResourceStatusPhase = "Active"
	// ResourceStatusPhaseFailed indicates the resource creation failed
	ResourceStatusPhaseFailed ResourceStatusPhase = "Failed"
	// ResourceStatusPhaseDeleting indicates the resource is being deleted
	ResourceStatusPhaseDeleting ResourceStatusPhase = "Deleting"
)

// ResourceStatus represents the lifecycle status of a managed sub-resource
type ResourceStatus struct {
	Phase   ResourceStatusPhase `json:"phase,omitempty"`
	Message string              `json:"message,omitempty"`
}

// EnvironmentStatus defines the observed state of Environment
type EnvironmentStatus struct {
	// Phase represents the current lifecycle phase of the environment
	// +optional
	Phase EnvironmentPhase `json:"phase,omitempty"`

	// Message provides a human-readable explanation for the current status
	// +optional
	Message string `json:"message,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed Environment
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastUpdated indicates when the status was last updated
	// +optional
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`

	// NamespaceStatus indicates the current state of the managed namespace
	// +optional
	NamespaceStatus *ResourceStatus `json:"namespaceStatus,omitempty"`

	// RoleBindingStatus indicates the current state of the managed role binding
	// +optional
	RoleBindingStatus *ResourceStatus `json:"roleBindingStatus,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="Current phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Namespaced,singular=environment,shortName=env
// +kubebuilder:validation:XValidation:rule="self.metadata.name == oldSelf.metadata.name",message="Environment name is immutable and cannot be changed after creation"
// +kubebuilder:validation:XValidation:rule="self.spec.id == oldSelf.spec.id",message="Environment ID is immutable and cannot be changed after creation"
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
